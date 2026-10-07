// Package imgateway is a 100% Go port of octop-gateway
// (multi-platform IM channel bridge for AI agents and bots).
//
// It provides a unified abstraction for building bots across multiple IM
// platforms (Feishu, QQ, WeChat, DingTalk, Discord, ...) with a single
// processor function.
//
// Quick start:
//
//	processor := func(ctx context.Context, msg *imgateway.InboundMessage) <-chan *imgateway.MessageEvent {
//	    events := make(chan *imgateway.MessageEvent, 16)
//	    go func() {
//	        defer close(events)
//	        events <- imgateway.TextMessage("Echo: " + msg.Text())
//	        events <- imgateway.CompletedEvent()
//	    }()
//	    return events
//	}
//	manager := imgateway.NewChannelManager(imgateway.ManagerOptions{Processor: processor})
//	_ = manager.Start(ctx)
package imgateway

import (
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Content Types
// ---------------------------------------------------------------------------

// ContentType enumerates the supported content types for message parts.
type ContentType string

const (
	ContentTypeText  ContentType = "text"
	ContentTypeImage ContentType = "image"
	ContentTypeVideo ContentType = "video"
	ContentTypeAudio ContentType = "audio"
	ContentTypeFile  ContentType = "file"
)

// ContentPart is one unified content part.
//
// The Python original used one pydantic model per kind (TextContent,
// ImageContent, ...) discriminated by "type". The Go port collapses them into
// a single struct with a Kind discriminator; fields that do not apply to a
// kind remain zero-valued. Behavior (e.g. the data / local_path / url byte
// source priority enforced by BaseChannel.LoadMediaBytes) is preserved
// exactly.
type ContentPart struct {
	Kind ContentType `json:"type"`

	// TextContent
	Text string `json:"text,omitempty"`

	// Shared media fields. Byte source priority (highest first):
	//  1. Data      — base64-encoded raw bytes
	//  2. LocalPath — key into the active MediaBackend
	//  3. URL       — remote source (fetched via BaseChannel.FetchRemoteMedia)
	URL       string `json:"url,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	Data      string `json:"data,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Size      *int64 `json:"size,omitempty"`

	// ImageContent
	AltText string `json:"alt_text,omitempty"`
	Width   *int   `json:"width,omitempty"`
	Height  *int   `json:"height,omitempty"`

	// VideoContent
	ThumbnailURL string `json:"thumbnail_url,omitempty"`

	// VideoContent / AudioContent (milliseconds)
	Duration *int `json:"duration,omitempty"`

	// FileContent
	Filename string `json:"filename,omitempty"`
}

// NewTextPart builds a text content part.
func NewTextPart(text string) ContentPart { return ContentPart{Kind: ContentTypeText, Text: text} }

// NewImagePart builds an image content part.
func NewImagePart(url string) ContentPart { return ContentPart{Kind: ContentTypeImage, URL: url} }

// NewVideoPart builds a video content part.
func NewVideoPart(url string) ContentPart { return ContentPart{Kind: ContentTypeVideo, URL: url} }

// NewAudioPart builds an audio content part.
func NewAudioPart(url string) ContentPart { return ContentPart{Kind: ContentTypeAudio, URL: url} }

// NewFilePart builds a file content part.
func NewFilePart(url, filename string) ContentPart {
	return ContentPart{Kind: ContentTypeFile, URL: url, Filename: filename}
}

// IsText reports whether the part is a text part.
func (p ContentPart) IsText() bool { return p.Kind == ContentTypeText }

// IsMedia reports whether the part is a media type (image/video/audio/file).
func (p ContentPart) IsMedia() bool {
	switch p.Kind {
	case ContentTypeImage, ContentTypeVideo, ContentTypeAudio, ContentTypeFile:
		return true
	}
	return false
}

// partURL returns the URL of a media part ("" for text parts). Mirrors the
// Python helpers that reached into media dataclass fields via getattr.
func (p ContentPart) partURL() string { return p.URL }

// ---------------------------------------------------------------------------
// Message Event Types
// ---------------------------------------------------------------------------

// MessageEventType enumerates the events emitted by the message processor.
type MessageEventType string

const (
	EventMessage       MessageEventType = "message"        // Complete message (send immediately)
	EventDelta         MessageEventType = "delta"          // Streaming text delta (accumulated, sent on COMPLETED)
	EventThinking      MessageEventType = "thinking"       // Complete thinking/reasoning block
	EventThinkingDelta MessageEventType = "thinking_delta" // Streaming thinking fragment
	EventFlush         MessageEventType = "flush"          // Flush accumulated content as one message, start new one
	EventTyping        MessageEventType = "typing"         // Typing indicator
	EventToolStart     MessageEventType = "tool_start"     // Tool/function call started
	EventToolEnd       MessageEventType = "tool_end"       // Tool/function call finished
	EventError         MessageEventType = "error"          // Error occurred
	EventCompleted     MessageEventType = "completed"      // End of response stream
)

// ---------------------------------------------------------------------------
// Group context models
// ---------------------------------------------------------------------------

// GroupContextMessage is one passive group message supplied as background for
// a later turn.
type GroupContextMessage struct {
	MessageID  string        `json:"message_id,omitempty"`
	SenderID   string        `json:"sender_id"`
	SenderName string        `json:"sender_name,omitempty"`
	Text       string        `json:"text"`
	Content    []ContentPart `json:"content,omitempty"`
	Timestamp  float64       `json:"timestamp"`
}

// GroupContext is the structured, short-lived group context attached to the
// current message.
type GroupContext struct {
	ConversationID     string                `json:"conversation_id"`
	Visibility         string                `json:"visibility"`
	Activation         string                `json:"activation"`
	Messages           []GroupContextMessage `json:"messages,omitempty"`
	CapabilityDegraded bool                  `json:"capability_degraded"`
}

// ---------------------------------------------------------------------------
// InboundMessage
// ---------------------------------------------------------------------------

// InboundMessage is an inbound message from user to bot (normalized from
// platform-native format).
//
// ChannelSubject is the reply/conversation target: a user for direct messages
// and the conversation ID for group messages. Group adapters retain the
// individual author in Metadata["sender_id"] / Metadata["sender_name"] so all
// members share one thread without losing attribution.
type InboundMessage struct {
	ChannelID        string            `json:"channel_id"`
	ChannelType      string            `json:"channel_type,omitempty"`
	TenantID         string            `json:"tenant_id,omitempty"`
	ChannelSubject   *ChannelSubject   `json:"channel_subject,omitempty"`
	ChannelSessionID string            `json:"channel_session_id,omitempty"`
	Content          []ContentPart     `json:"content"`
	GroupContext     *GroupContext     `json:"group_context,omitempty"`
	Metadata         map[string]any    `json:"metadata,omitempty"`
	Timestamp        float64           `json:"timestamp"`
}

// NewInboundMessage builds an InboundMessage with the timestamp defaulted to
// now (mirrors the pydantic default_factory).
func NewInboundMessage(channelID string, content []ContentPart) *InboundMessage {
	return &InboundMessage{
		ChannelID: channelID,
		Content:   content,
		Metadata:  map[string]any{},
		Timestamp: nowFloat(),
	}
}

// Text extracts concatenated text from all text parts, joined by "\n".
func (m *InboundMessage) Text() string {
	parts := make([]string, 0, len(m.Content))
	for _, c := range m.Content {
		if c.Kind == ContentTypeText && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// HasMedia reports whether the message contains any media part.
func (m *InboundMessage) HasMedia() bool {
	for _, c := range m.Content {
		if c.IsMedia() {
			return true
		}
	}
	return false
}

// Clone returns a shallow copy of the message (metadata map is copied
// one level, mirroring model_copy semantics used by MergeMessages).
func (m *InboundMessage) Clone() *InboundMessage {
	cp := *m
	cp.Content = append([]ContentPart(nil), m.Content...)
	meta := make(map[string]any, len(m.Metadata)+2)
	for k, v := range m.Metadata {
		meta[k] = v
	}
	cp.Metadata = meta
	return &cp
}

// ---------------------------------------------------------------------------
// MessageEvent
// ---------------------------------------------------------------------------

// MessageEvent is an event emitted by the message processor back to the
// channel for delivery.
type MessageEvent struct {
	Type     MessageEventType `json:"type"`
	Content  []ContentPart    `json:"content,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// TextMessage creates a MESSAGE event with a single text content part.
func TextMessage(text string) *MessageEvent {
	return &MessageEvent{Type: EventMessage, Content: []ContentPart{NewTextPart(text)}}
}

// DeltaEvent creates a DELTA event (streaming text fragment).
//
// Deltas are accumulated by the channel and merged into a single message when
// COMPLETED is received. Use this for token-by-token LLM output.
func DeltaEvent(text string) *MessageEvent {
	return &MessageEvent{Type: EventDelta, Content: []ContentPart{NewTextPart(text)}}
}

// TypingEvent creates a TYPING indicator event.
func TypingEvent() *MessageEvent { return &MessageEvent{Type: EventTyping} }

// FlushEvent creates a FLUSH event — sends accumulated content as one message
// immediately.
func FlushEvent() *MessageEvent { return &MessageEvent{Type: EventFlush} }

// ThinkingEvent creates a THINKING event (complete reasoning block).
func ThinkingEvent(text string) *MessageEvent {
	return &MessageEvent{Type: EventThinking, Content: []ContentPart{NewTextPart(text)}}
}

// ThinkingDeltaEvent creates a THINKING_DELTA event (streaming reasoning
// fragment).
func ThinkingDeltaEvent(text string) *MessageEvent {
	return &MessageEvent{Type: EventThinkingDelta, Content: []ContentPart{NewTextPart(text)}}
}

// ToolStartEvent creates a TOOL_START event (tool/function call began).
// Extra metadata entries are merged on top of tool_name.
func ToolStartEvent(toolName string, extra map[string]any) *MessageEvent {
	meta := map[string]any{"tool_name": toolName}
	for k, v := range extra {
		if k == "tool_name" {
			continue
		}
		meta[k] = v
	}
	return &MessageEvent{Type: EventToolStart, Metadata: meta}
}

// ToolEndEvent creates a TOOL_END event (tool/function call finished).
func ToolEndEvent(toolName string, extra map[string]any) *MessageEvent {
	meta := map[string]any{"tool_name": toolName}
	for k, v := range extra {
		if k == "tool_name" {
			continue
		}
		meta[k] = v
	}
	return &MessageEvent{Type: EventToolEnd, Metadata: meta}
}

// CompletedEvent creates a COMPLETED event signaling end of response.
func CompletedEvent() *MessageEvent { return &MessageEvent{Type: EventCompleted} }

// ErrorEvent creates an ERROR event.
func ErrorEvent(message string) *MessageEvent {
	return &MessageEvent{Type: EventError, Error: message}
}

// WithContent attaches content parts to the event (fluent).
func (e *MessageEvent) WithContent(parts ...ContentPart) *MessageEvent {
	e.Content = append(e.Content, parts...)
	return e
}

// WithMetadata merges metadata entries into the event (fluent).
func (e *MessageEvent) WithMetadata(meta map[string]any) *MessageEvent {
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	for k, v := range meta {
		e.Metadata[k] = v
	}
	return e
}

// ---------------------------------------------------------------------------
// Subject Registry Model
// ---------------------------------------------------------------------------

// ChannelSubject is a known outbound routing subject (a direct user or group
// conversation).
//
// Automatically collected on first message. Used for proactive push and as
// the unified routing handle for all outbound send operations.
type ChannelSubject struct {
	SubjectID   string         `json:"subject_id"`
	FirstSeen   float64        `json:"first_seen,omitempty"`
	LastSeen    float64        `json:"last_seen,omitempty"`
	DisplayName string         `json:"display_name,omitempty"`
	ChatType    string         `json:"chat_type,omitempty"` // "direct", "group", ...
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// NewChannelSubject builds a subject with defaulted timestamps.
func NewChannelSubject(subjectID string) *ChannelSubject {
	now := nowFloat()
	return &ChannelSubject{SubjectID: subjectID, Metadata: map[string]any{}, FirstSeen: now, LastSeen: now}
}

// Clone returns a deep-enough copy of the subject (metadata copied one level).
func (s *ChannelSubject) Clone() *ChannelSubject {
	cp := *s
	meta := make(map[string]any, len(s.Metadata)+2)
	for k, v := range s.Metadata {
		meta[k] = v
	}
	cp.Metadata = meta
	return &cp
}

// nowFloat returns the current unix time as float seconds.
func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }
