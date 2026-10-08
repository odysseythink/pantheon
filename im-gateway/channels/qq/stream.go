// Package qq: this file ports stream.py — the C2C replace-mode stream
// session for QQ official “stream_messages“.
//
// Mirrors the dsh-qqbot-community OutboundPipeline drain: one in-flight
// frame, throttled full-text replace, then a DONE frame. Token fragments
// from Octop are accumulated by the caller; this module only reconciles
// prefixes and serializes HTTP frames.
package qq

import (
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Stream state values for QQ stream_messages.
const (
	StreamGenerating = 1
	StreamDone       = 10
)

var (
	streamSeqMu sync.Mutex
	streamSeq   = 0
)

// nextStreamMsgSeq is the process-wide msg_seq in 0..65535 (one value per
// C2C stream).
func nextStreamMsgSeq() int {
	streamSeqMu.Lock()
	defer streamSeqMu.Unlock()
	streamSeq = (streamSeq + 1) % 65536
	return streamSeq
}

var streamStart = time.Now()

func monotonicNow() float64 { return time.Since(streamStart).Seconds() }

func collapseWS(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// prefixMatches reports whether incoming continues or equals accepted
// (whitespace-tolerant).
func prefixMatches(accepted, incoming string) bool {
	if strings.HasPrefix(incoming, accepted) {
		return true
	}
	if accepted == "" {
		return true
	}
	// A newline-only hold must stay; collapsing it to "" would drop the prefix.
	if strings.TrimSpace(accepted) == "" {
		return false
	}
	// Incoming that dropped a leading newline is not a legal replace prefix.
	if accepted[0] == '\r' || accepted[0] == '\n' {
		if incoming == "" || (incoming[0] != '\r' && incoming[0] != '\n') {
			return false
		}
	}
	return strings.HasPrefix(collapseWS(incoming), collapseWS(accepted))
}

// longestCommonPrefixBytes returns the length of the common byte prefix of
// left and right, adjusted down to a UTF-8 rune boundary. This matches the
// Python codepoint-wise comparison for valid UTF-8 input.
func longestCommonPrefixBytes(left, right string) int {
	n := len(left)
	if len(right) < n {
		n = len(right)
	}
	i := 0
	for i < n && left[i] == right[i] {
		i++
	}
	for i > 0 && !utf8.RuneStart(right[i]) {
		i--
	}
	return i
}

// reconcileStreamText builds the next replace-mode full text from the last
// accepted frame.
func reconcileStreamText(accepted, incoming string) string {
	if accepted == "" {
		return incoming
	}
	if prefixMatches(accepted, incoming) {
		return incoming
	}
	if prefixMatches(incoming, accepted) {
		return accepted
	}
	lcp := longestCommonPrefixBytes(accepted, incoming)
	return accepted + incoming[lcp:]
}

// StreamFrame is one QQ stream_messages replace frame.
type StreamFrame struct {
	UserID      string
	MsgID       string
	MsgSeq      int
	Index       int
	Text        string
	State       int
	StreamMsgID string // empty when not yet assigned by the server
}

// SendFrameFunc posts one replace frame; it returns the server-assigned
// stream msg id (empty when absent). Transport errors are returned and mark
// the session failed.
type SendFrameFunc func(frame *StreamFrame) (string, error)

// StreamSession is the single-slot drain for one C2C inbound turn.
type StreamSession struct {
	mu          sync.Mutex
	drainWG     sync.WaitGroup
	userID      string
	msgID       string
	msgSeq      int
	throttleMS  int
	doneRetries int
	sendFrame   SendFrameFunc

	index        int
	lastAccepted string
	pending      *string // nil = nothing queued
	inFlight     bool
	closing      bool
	streamMsgID  string
	sentFrames   int
	failed       bool
	lastSentAt   float64
	inflightText *string
}

// NewStreamSession builds a session (mirrors StreamSession.__init__).
func NewStreamSession(userID, msgID string, throttleMS, doneRetries int, sendFrame SendFrameFunc) *StreamSession {
	if throttleMS < 0 {
		throttleMS = 0
	}
	if doneRetries < 1 {
		doneRetries = 1
	}
	return &StreamSession{
		userID:      userID,
		msgID:       msgID,
		msgSeq:      nextStreamMsgSeq(),
		throttleMS:  throttleMS,
		doneRetries: doneRetries,
		sendFrame:   sendFrame,
	}
}

func (s *StreamSession) prefixBaseLocked() string {
	locked := s.lastAccepted
	if s.inflightText != nil {
		locked = *s.inflightText
	}
	if s.pending != nil {
		return reconcileStreamText(locked, *s.pending)
	}
	return locked
}

// PrefixBase exposes the locked prefix reconciliation base (mirrors
// _prefix_base, used by the channel's _locked_stream_text).
func (s *StreamSession) PrefixBase() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefixBaseLocked()
}

// Offer records the latest candidate full text and kicks the drain loop.
func (s *StreamSession) Offer(text string) {
	s.mu.Lock()
	if s.failed || s.closing {
		s.mu.Unlock()
		return
	}
	reconciled := reconcileStreamText(s.prefixBaseLocked(), text)
	s.pending = &reconciled
	kick := !s.inFlight && !s.failed && !s.closing
	if kick {
		s.kickLocked()
	}
	s.mu.Unlock()
}

// DiscardUnsent drops queued visible text. Keeps an unsent whitespace hold.
func (s *StreamSession) DiscardUnsent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.closing {
		return
	}
	locked := s.lastAccepted
	if s.inflightText != nil {
		locked = *s.inflightText
	}
	if strings.TrimSpace(locked) == "" {
		if locked == "" {
			locked = "\n"
		}
		s.pending = &locked
		return
	}
	s.pending = nil
}

// Dirty reports whether this session has sent or queued text.
func (s *StreamSession) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sentFrames > 0 || s.pending != nil
}

// Failed reports whether a frame send failed (session is dead).
func (s *StreamSession) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Closing reports whether the session is closing/closed.
func (s *StreamSession) Closing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// LastAccepted returns the last successfully sent full text.
func (s *StreamSession) LastAccepted() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAccepted
}

// SentFrames returns the number of successfully sent frames.
func (s *StreamSession) SentFrames() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sentFrames
}

// kickLocked starts the drain goroutine. Caller must hold s.mu.
func (s *StreamSession) kickLocked() {
	s.drainWG.Add(1)
	go func() {
		defer s.drainWG.Done()
		s.drain()
	}()
}

// drain sends queued content one replace frame at a time.
func (s *StreamSession) drain() {
	s.mu.Lock()
	if s.inFlight || s.failed || s.closing || s.pending == nil {
		s.mu.Unlock()
		return
	}
	s.inFlight = true
	for !s.failed && !s.closing && s.pending != nil {
		wait := float64(s.throttleMS)/1000.0 - (monotonicNow() - s.lastSentAt)
		if wait > 0 {
			s.mu.Unlock()
			time.Sleep(time.Duration(wait * float64(time.Second)))
			s.mu.Lock()
			continue
		}
		text := *s.pending
		inflight := text
		s.inflightText = &inflight
		s.pending = nil
		s.mu.Unlock()
		s.emit(text, StreamGenerating)
		s.mu.Lock()
	}
	s.inFlight = false
	kick := !s.failed && !s.closing && s.pending != nil
	if kick {
		s.kickLocked()
	}
	s.mu.Unlock()
}

// waitIdle waits until no drain goroutine is running (mirrors _wait_idle:
// await the drain task, then poll in_flight).
func (s *StreamSession) waitIdle() {
	s.drainWG.Wait()
	for {
		s.mu.Lock()
		in := s.inFlight
		s.mu.Unlock()
		if !in {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Finish closes the stream. Successful turns flush pending first; abort
// (discardPending) drops it.
func (s *StreamSession) Finish(text string, discardPending bool) {
	if discardPending {
		s.mu.Lock()
		s.closing = true
		s.pending = nil
		s.mu.Unlock()
	} else {
		s.waitIdle()
		s.mu.Lock()
		if s.pending != nil && !s.failed && !s.inFlight && !s.closing {
			s.kickLocked()
		}
		s.mu.Unlock()
		s.waitIdle()
		s.mu.Lock()
		s.closing = true
		s.pending = nil
		s.mu.Unlock()
	}
	s.waitIdle()

	s.mu.Lock()
	if s.sentFrames <= 0 {
		s.mu.Unlock()
		return
	}
	payload := text
	if payload == "" {
		payload = s.lastAccepted
	}
	retries := s.doneRetries
	s.mu.Unlock()

	for attempt := 1; attempt <= retries; attempt++ {
		if s.emit(payload, StreamDone) {
			return
		}
		if attempt == retries {
			return
		}
		log.Printf("imgateway/qq WARN QQ DONE frame attempt %d failed; retrying", attempt)
		time.Sleep(time.Duration(0.4 * float64(attempt) * float64(time.Second)))
	}
}

// emit posts one frame. Returns true on success; a send failure marks the
// whole session failed (the frame sender owns transport errors).
func (s *StreamSession) emit(text string, state int) bool {
	s.mu.Lock()
	frame := &StreamFrame{
		UserID:      s.userID,
		MsgID:       s.msgID,
		MsgSeq:      s.msgSeq,
		Index:       s.index,
		Text:        text,
		State:       state,
		StreamMsgID: s.streamMsgID,
	}
	inflight := text
	s.inflightText = &inflight
	s.mu.Unlock()

	streamMsgID, err := s.sendFrame(frame)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.failed = true
		if s.inflightText != nil && *s.inflightText == text {
			s.inflightText = nil
		}
		log.Printf("imgateway/qq WARN QQ stream frame failed; falling back to static delivery: %v", err)
		return false
	}
	s.index++
	s.lastAccepted = text
	s.lastSentAt = monotonicNow()
	s.sentFrames++
	if streamMsgID != "" && s.streamMsgID == "" {
		s.streamMsgID = streamMsgID
	}
	if s.inflightText != nil && *s.inflightText == text {
		s.inflightText = nil
	}
	return true
}
