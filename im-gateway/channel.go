package imgateway

// BaseChannel abstract class and channel configuration.
//
// Defines the contract that all IM platform channel implementations must
// follow. Provides shared logic for message processing, debouncing, and
// lifecycle management. Also exports ChannelConfig — the base struct for all
// platform-specific Config structs.

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// MessageProcessor takes an InboundMessage and yields MessageEvents.
//
// The Python original was an async iterator; the Go port models it as a
// function returning a channel of events that is closed when the stream ends.
// A failing processor must emit an ErrorEvent (or panic — the channel
// recovers panics into the shared error path).
type MessageProcessor func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent

// SafeProcessor wraps a MessageProcessor so that a panic inside the
// producing goroutine is converted into the shared Python error path:
// "An error occurred while processing your message." (the Python except
// branch also flushed accumulated deltas before the error text; in the Go
// channel model the producer cannot reach the channel's buffers, so hosts
// that need the flush should emit DeltaEvents before failing).
func SafeProcessor(processor MessageProcessor) MessageProcessor {
	return func(ctx context.Context, msg *InboundMessage) (events <-chan *MessageEvent) {
		out := make(chan *MessageEvent, 16)
		go func() {
			defer close(out)
			defer func() {
				if r := recover(); r != nil {
					logError("processor error: session=%s panic=%v", msg.ChannelSessionID, r)
					out <- ErrorEvent("An error occurred while processing your message.")
				}
			}()
			for event := range processor(ctx, msg) {
				out <- event
			}
		}()
		return out
	}
}

// ---------------------------------------------------------------------------
// ChannelConfig — base struct for all channel configuration structs
// ---------------------------------------------------------------------------

// ChannelConfig is the base config embedded by every platform-specific
// config struct, so callers can always set ChannelID and TenantID directly.
type ChannelConfig struct {
	// ChannelID is an opaque UUID used as the registration key inside
	// ChannelManager. When empty the manager auto-generates a uuid4 hex.
	ChannelID string `json:"channel_id,omitempty"`
	// TenantID is an optional tenant identifier for multi-tenant deployments.
	TenantID string `json:"tenant_id,omitempty"`
	// ShowThinking forwards reasoning content to the user.
	ShowThinking bool `json:"show_thinking,omitempty"`
	// ShowToolHints forwards tool call status to the user.
	ShowToolHints bool `json:"show_tool_hints"`
	// GroupContext holds the group activation policy.
	GroupContext *GroupContextConfig `json:"group_context,omitempty"`
}

// Normalize fills defaults on the embedded base config (parsed from dict).
func (c *ChannelConfig) Normalize() {
	if c.GroupContext == nil {
		c.GroupContext = NewGroupContextConfig()
	}
}

// MissingCredentials returns the required credential field names that are
// empty. Platform configs with non-field requirements override MissingCredentials.
func (c *ChannelConfig) MissingCredentials() []string { return nil }

// DictConfig is implemented by platform config structs that can be built
// from a plain dict (mirrors ChannelConfig.from_dict).
type DictConfig interface {
	FromDict(data map[string]any) error
}

// ---------------------------------------------------------------------------
// ChannelCredentialsError
// ---------------------------------------------------------------------------

// ChannelCredentialsError is raised when a channel config is missing required
// credentials. Carries structured Kind / Missing so the embedding application
// can render a localized message.
type ChannelCredentialsError struct {
	Kind    string
	Missing []string
}

func (e *ChannelCredentialsError) Error() string {
	return fmt.Sprintf("channel %q missing required credentials: %s", e.Kind, strings.Join(e.Missing, ", "))
}

// ---------------------------------------------------------------------------
// MIME helpers
// ---------------------------------------------------------------------------

var mimeTypeExtMap = map[string]string{
	"image/png":         ".png",
	"image/jpeg":        ".jpg",
	"image/gif":         ".gif",
	"image/webp":        ".webp",
	"image/bmp":         ".bmp",
	"video/mp4":         ".mp4",
	"video/webm":        ".webm",
	"audio/mpeg":        ".mp3",
	"audio/mp3":         ".mp3",
	"audio/wav":         ".wav",
	"audio/ogg":         ".ogg",
	"audio/silk":        ".silk",
	"application/pdf":   ".pdf",
}

// mimeToExt converts a mime type to a file extension.
func mimeToExt(mimeType, defaultExt string) string {
	if mimeType == "" {
		return defaultExt
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if ext, ok := mimeTypeExtMap[base]; ok {
		return ext
	}
	return defaultExt
}

// guessContentType guesses a MIME type from a URL or filesystem path.
func guessContentType(urlOrPath string) string {
	u, err := url.Parse(urlOrPath)
	path := urlOrPath
	if err == nil && u.Path != "" {
		path = u.Path
	}
	ext := filepath.Ext(path)
	if ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return strings.Split(t, ";")[0]
		}
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// Channel implementation contract
// ---------------------------------------------------------------------------

// ChannelImpl is the platform-specific contract that every channel struct
// must implement (mirrors the Python abstract methods).
type ChannelImpl interface {
	// Start starts the channel (open connections, register webhooks, etc.).
	Start(ctx context.Context) error
	// Stop stops the channel (close connections, cleanup resources).
	Stop(ctx context.Context) error
	// SendText is the platform-native text send. Implementations must NOT
	// call ReplyText/PushText — call sibling Send* helpers instead so the
	// public throttle stays at one acquire per subject-visible message.
	SendText(ctx context.Context, subject *ChannelSubject, text string) error
	// SendContent is the platform-native rich content send.
	SendContent(ctx context.Context, subject *ChannelSubject, parts []ContentPart) error
	// SendMedia is the platform-native media send.
	SendMedia(ctx context.Context, subject *ChannelSubject, part ContentPart) error
	// ParseInbound parses a platform-native payload into InboundMessage.
	ParseInbound(ctx context.Context, raw any) (*InboundMessage, error)
}

// TypingSender is an optional capability for platforms supporting typing
// indicators (WeChat, Discord, ...).
type TypingSender interface {
	SendTypingIndicator(ctx context.Context, subject *ChannelSubject)
}

// PushMetadataEnricher is an optional platform hook to backfill routing
// fields for sparse proactive subjects.
type PushMetadataEnricher interface {
	EnrichPushMetadata(subject *ChannelSubject, meta map[string]any) map[string]any
}

// Preprocessor is an optional hook to normalize platform-specific content
// before shared policy runs (e.g. decrypting inbound media).
type Preprocessor interface {
	PreprocessInbound(ctx context.Context, message *InboundMessage)
}

// ProcessInboundOverride is the optional per-channel output strategy. Streaming
// or protocol-specialized adapters implement this while retaining the shared
// preparation and finalization lifecycle.
type ProcessInboundOverride interface {
	ProcessInbound(ctx context.Context, message *InboundMessage, subject *ChannelSubject) bool
}

// RemoteMediaFetcher is an optional override to inject platform auth headers
// for downloads from the platform's own media API.
type RemoteMediaFetcher interface {
	FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error)
}

// DebounceKeyer is an optional override for the debounce/session-lock key.
type DebounceKeyer interface {
	GetDebounceKey(message *InboundMessage) string
}

// InboundBatcher is an optional override for manager-level merge decisions.
type InboundBatcher interface {
	ShouldBatchInbound(message *InboundMessage) bool
	MergeInbound(messages []*InboundMessage) *InboundMessage
}

// BaseOptions configures BaseChannel construction.
type BaseOptions struct {
	ChannelType     string
	Processor       MessageProcessor
	ChannelID       string
	TenantID        string
	DebounceSeconds float64
	Constraints     *ChannelConstraints
	Config          *ChannelConfig
}

// BaseChannel is the shared inbound/outbound machinery every channel embeds.
type BaseChannel struct {
	self ChannelImpl

	channelType     string
	processor       MessageProcessor
	channelID       string
	tenantID        string
	mediaBackend    MediaBackend
	debounceSeconds float64
	constraints     *ChannelConstraints
	rateLimiter     *RateLimiter
	enqueueCallback func(payload any)
	groupContext    *GroupContextManager

	httpMu    sync.Mutex
	http      *http.Client
	httpOwned bool

	subjectsMu    sync.Mutex
	knownSubjects map[string]*ChannelSubject
	onNewSubject  func(*ChannelSubject)
}

// InitBase wires the base channel. Call from each channel constructor with
// the outer struct as impl.
func (b *BaseChannel) InitBase(opts BaseOptions, impl ChannelImpl) {
	b.self = impl
	b.channelType = opts.ChannelType
	b.processor = opts.Processor
	b.channelID = opts.ChannelID
	if b.channelID == "" {
		b.channelID = newUUIDHex()
	}
	b.tenantID = opts.TenantID
	b.debounceSeconds = opts.DebounceSeconds

	cfg := opts.Config
	var gcCfg *GroupContextConfig
	if cfg != nil && cfg.GroupContext != nil {
		gcCfg = cfg.GroupContext
	}
	b.groupContext = NewGroupContextManager(gcCfg)

	if opts.Constraints != nil {
		b.constraints = opts.Constraints
	} else {
		if dc, ok := impl.(DefaultConstraintsProvider); ok {
			b.constraints = dc.DefaultConstraints()
		} else {
			b.constraints = NewChannelConstraints()
		}
	}
	if cfg != nil {
		b.constraints.ShowThinking = cfg.ShowThinking
		b.constraints.ShowToolHints = cfg.ShowToolHints
	}
	b.rateLimiter = nil
	if b.constraints.SendRateLimitMax > 0 {
		b.rateLimiter = NewRateLimiter(b.constraints.SendRateLimitMax, b.constraints.SendRateLimitWindow)
	}

	b.knownSubjects = map[string]*ChannelSubject{}
}

// DefaultConstraintsProvider lets a channel declare its default constraints.
type DefaultConstraintsProvider interface {
	DefaultConstraints() *ChannelConstraints
}

// ChannelType returns the channel type string ("feishu", "qq", ...).
func (b *BaseChannel) ChannelType() string { return b.channelType }

// ChannelID returns the unique instance ID for this channel.
func (b *BaseChannel) ChannelID() string { return b.channelID }

// TenantID returns the tenant identifier.
func (b *BaseChannel) TenantID() string { return b.tenantID }

// Constraints exposes channel constraints for dynamic runtime modification.
func (b *BaseChannel) Constraints() *ChannelConstraints { return b.constraints }

// GroupContextManager exposes group policy state for adapter capability-change
// hooks.
func (b *BaseChannel) GroupContextManager() *GroupContextManager { return b.groupContext }

// Processor returns the bound message processor.
func (b *BaseChannel) Processor() MessageProcessor { return b.processor }

// SetMediaBackend sets the media persistence backend. Called by
// ChannelManager before Start.
func (b *BaseChannel) SetMediaBackend(backend MediaBackend) { b.mediaBackend = backend }

// MediaBackend returns the active media backend, if configured.
func (b *BaseChannel) MediaBackend() MediaBackend { return b.mediaBackend }

// DebounceSeconds returns the configured debounce window.
func (b *BaseChannel) DebounceSeconds() float64 { return b.debounceSeconds }

// ---------------------------------------------------------------------------
// Shared HTTP client (mirrors the lazy aiohttp session)
// ---------------------------------------------------------------------------

// HTTPClient lazily creates the shared HTTP client.
func (b *BaseChannel) HTTPClient() *http.Client {
	b.httpMu.Lock()
	defer b.httpMu.Unlock()
	if b.http == nil {
		b.http = &http.Client{Timeout: 60 * time.Second}
		b.httpOwned = true
	}
	return b.http
}

// CloseHTTP closes/references-clears the shared HTTP client if open.
func (b *BaseChannel) CloseHTTP() {
	b.httpMu.Lock()
	defer b.httpMu.Unlock()
	// net/http has no explicit close; drop the reference so the next call
	// creates a fresh client. Transport keep-alives expire on their own.
	b.http = nil
}

// ---------------------------------------------------------------------------
// Outbound API
//
// Three layers:
//   - Public Reply* (in inbound context) — applies rate-limit, then delegates
//     to the platform primitive. Routing is fully determined by the
//     ChannelSubject object.
//   - Public Push* (bot-initiated, no inbound context) — same throttle, calls
//     into SendContent directly to keep "one call, one acquire".
//   - ChannelImpl Send* (platform primitives) — channels implement these.
//     Channels MUST call sibling Send* helpers from inside their own code,
//     never the public Reply* names, otherwise a single user-visible message
//     would acquire the rate slot multiple times.
// ---------------------------------------------------------------------------

func (b *BaseChannel) acquireRateSlot(ctx context.Context) {
	if b.rateLimiter != nil {
		b.rateLimiter.Acquire(ctx)
	}
}

// ReplyText replies with a plain text message in the context of an inbound
// message.
func (b *BaseChannel) ReplyText(ctx context.Context, subject *ChannelSubject, text string) error {
	b.acquireRateSlot(ctx)
	return b.self.SendText(ctx, subject, text)
}

// ReplyContent replies with rich content (text + media).
func (b *BaseChannel) ReplyContent(ctx context.Context, subject *ChannelSubject, parts []ContentPart) error {
	b.acquireRateSlot(ctx)
	return b.self.SendContent(ctx, subject, parts)
}

// ReplyMedia replies with a single media item.
func (b *BaseChannel) ReplyMedia(ctx context.Context, subject *ChannelSubject, part ContentPart) error {
	b.acquireRateSlot(ctx)
	return b.self.SendMedia(ctx, subject, part)
}

// ResolvePushSubject prepares subject for proactive push (cron, webhooks,
// admin, ...). Merges in-memory inbound routing state, applies platform
// backfill, and strips ephemeral passive-reply fields so sends use proactive
// paths.
func (b *BaseChannel) ResolvePushSubject(subject *ChannelSubject) *ChannelSubject {
	meta := map[string]any{}
	for k, v := range subject.Metadata {
		meta[k] = v
	}
	b.subjectsMu.Lock()
	known := b.knownSubjects[subject.SubjectID]
	b.subjectsMu.Unlock()
	if known != nil {
		for k, v := range known.Metadata {
			if _, ok := meta[k]; !ok {
				meta[k] = v
			}
		}
	}
	if enricher, ok := b.self.(PushMetadataEnricher); ok {
		meta = enricher.EnrichPushMetadata(subject, meta)
	}
	StripEphemeralPushMeta(meta)

	chatType := subject.ChatType
	if chatType == "" && known != nil {
		chatType = known.ChatType
	}
	firstSeen := subject.FirstSeen
	if firstSeen == 0 && known != nil {
		firstSeen = known.FirstSeen
	}
	lastSeen := subject.LastSeen
	if lastSeen == 0 && known != nil {
		lastSeen = known.LastSeen
	}
	return &ChannelSubject{
		SubjectID: subject.SubjectID,
		ChatType:  chatType,
		Metadata:  meta,
		FirstSeen: firstSeen,
		LastSeen:  lastSeen,
	}
}

// PushMessage sends a bot-initiated message (no prior user request).
//
// Routes through the same rate-limit slot as ReplyContent so proactive bursts
// cannot bypass per-channel throttling.
func (b *BaseChannel) PushMessage(ctx context.Context, subject *ChannelSubject, parts []ContentPart) error {
	b.acquireRateSlot(ctx)
	resolved := b.ResolvePushSubject(subject)
	return b.self.SendContent(ctx, resolved, parts)
}

// PushText sends a text-only message proactively.
func (b *BaseChannel) PushText(ctx context.Context, subject *ChannelSubject, text string) error {
	b.acquireRateSlot(ctx)
	resolved := b.ResolvePushSubject(subject)
	return b.self.SendText(ctx, resolved, text)
}

// ParseInbound delegates to the platform implementation.
func (b *BaseChannel) ParseInbound(ctx context.Context, raw any) (*InboundMessage, error) {
	return b.self.ParseInbound(ctx, raw)
}

// ---------------------------------------------------------------------------
// Media — fetch (inbound) / load (outbound)
// ---------------------------------------------------------------------------

// FetchRemoteMediaDefault downloads a remote URL and returns
// (bytes, contentType) with a plain HTTP GET — the shared default used when
// the channel does not implement RemoteMediaFetcher.
//
// NOTE: BaseChannel intentionally does not expose a method named
// FetchRemoteMedia: a same-named default would be promoted onto every
// embedding channel and make the RemoteMediaFetcher type assertion succeed
// recursively. Channels implementing platform-auth fetching define their own
// FetchRemoteMedia (satisfying RemoteMediaFetcher) and dispatch through
// fetchRemote below.
func (b *BaseChannel) FetchRemoteMediaDefault(ctx context.Context, rawURL string) ([]byte, string, error) {
	return b.fetchRemoteMediaDefault(ctx, rawURL)
}

// fetchRemote dispatches to the channel's RemoteMediaFetcher when the outer
// struct defines FetchRemoteMedia, falling back to the plain GET default.
func (b *BaseChannel) fetchRemote(ctx context.Context, rawURL string) ([]byte, string, error) {
	if f, ok := b.self.(RemoteMediaFetcher); ok {
		return f.FetchRemoteMedia(ctx, rawURL)
	}
	return b.fetchRemoteMediaDefault(ctx, rawURL)
}

func (b *BaseChannel) fetchRemoteMediaDefault(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := b.HTTPClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, rawURL)
	}
	ct := resp.Header.Get("Content-Type")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	if ct == "" {
		ct = guessContentType(rawURL)
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return buf, ct, nil
}

// LoadMediaBytes resolves a content part to (bytes, contentType).
//
// Resolution order:
//  1. part.Data set → decode the base64 payload (zero I/O).
//  2. part.LocalPath set → read from the configured MediaBackend. A
//     LocalPath without a backend is a configuration error.
//  3. part.URL set → fetch via FetchRemoteMedia and, when a backend is
//     configured, cache the result back to the backend so subsequent reads
//     are local.
//
// Returns an error if none of the three fields is set.
func (b *BaseChannel) LoadMediaBytes(part *ContentPart) ([]byte, string, error) {
	if part.Kind == ContentTypeText {
		return nil, "", fmt.Errorf("load_media_bytes called on TextContent")
	}

	// Priority 1: inline base64 bytes — the source of truth, no I/O.
	if part.Data != "" {
		mimeType := part.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		raw, err := base64.StdEncoding.DecodeString(part.Data)
		if err != nil {
			return nil, "", fmt.Errorf("invalid base64 in ContentPart.data: %w", err)
		}
		return raw, mimeType, nil
	}

	// Priority 2: MediaBackend key.
	if part.LocalPath != "" {
		if b.mediaBackend == nil {
			return nil, "", fmt.Errorf(
				"ContentPart.local_path is set but no MediaBackend is configured for channel %s — "+
					"call ChannelManager(media_backend=...) or channel.SetMediaBackend(...) before send",
				b.channelID)
		}
		raw, err := b.mediaBackend.Read(context.Background(), part.LocalPath)
		if err != nil {
			return nil, "", err
		}
		mimeType := part.MimeType
		if mimeType == "" {
			mimeType = guessContentType(part.LocalPath)
		}
		return raw, mimeType, nil
	}

	// Priority 3: remote URL.
	rawURL := part.URL
	if rawURL == "" {
		return nil, "", fmt.Errorf("ContentPart has no data/local_path/url")
	}

	raw, mimeType, err := b.fetchRemote(context.Background(), rawURL)
	if err != nil {
		return nil, "", err
	}

	// Cache to backend so future reads are local and downstream consumers
	// see a stable local_path even for URL-only parts.
	if b.mediaBackend != nil {
		key := b.deriveStorageKey(part, mimeType)
		if saveErr := b.mediaBackend.Save(context.Background(), raw, key); saveErr != nil {
			// MediaBackend is a host-provided extension; cache failures are
			// non-fatal.
			logDebug("failed to cache fetched media to backend: %v", saveErr)
		} else {
			part.LocalPath = key
		}
	}
	return raw, mimeType, nil
}

// ---------------------------------------------------------------------------
// Media auto-persist on inbound
// ---------------------------------------------------------------------------

// PersistMedia downloads remote media and persists via the configured backend.
//
// For each non-text content part with a URL but no LocalPath: download via
// FetchRemoteMedia (platform auth applied), save bytes to the backend under a
// deterministic key, and stamp part.LocalPath with the backend key.
func (b *BaseChannel) PersistMedia(ctx context.Context, message *InboundMessage) {
	if b.mediaBackend == nil {
		return
	}
	ts := time.Now().Unix()

	groups := [][]ContentPart{message.Content}
	if message.GroupContext != nil {
		for _, item := range message.GroupContext.Messages {
			groups = append(groups, item.Content)
		}
	}

	index := 0
	for _, parts := range groups {
		for i := range parts {
			b.persistMediaPart(ctx, &parts[i], index, ts)
			index++
		}
	}
}

// persistMediaPart ... (dispatch via fetchRemote) ...
func (b *BaseChannel) persistMediaPart(ctx context.Context, part *ContentPart, index int, timestamp int64) {
	if b.mediaBackend == nil {
		return
	}
	if part.Kind == ContentTypeText || part.LocalPath != "" {
		return
	}
	rawURL := part.URL
	if rawURL == "" {
		return
	}

	filename := resolveMediaFilename(part, index, timestamp)
	key := fmt.Sprintf("%s/%s/%s", b.channelType, b.channelID, filename)

	data, mimeType, err := b.fetchRemote(ctx, rawURL)
	if err != nil {
		logWarn("failed to persist media: url=%s err=%v", truncate(rawURL, 100), err)
		return
	}
	if err := b.mediaBackend.Save(ctx, data, key); err != nil {
		logWarn("failed to persist media: url=%s err=%v", truncate(rawURL, 100), err)
		return
	}
	part.LocalPath = key
	if part.Size == nil {
		size := int64(len(data))
		part.Size = &size
	}
	if part.MimeType == "" {
		part.MimeType = mimeType
	}
	logInfo("media persisted: %s (%d bytes) -> key=%s", part.Kind, len(data), key)
}

// SafeMediaFilename returns a storage-safe basename while retaining Unicode
// names.
func (b *BaseChannel) SafeMediaFilename(filename, fallback string) string {
	basename := filename
	if idx := strings.LastIndex(basename, "/"); idx >= 0 {
		basename = basename[idx+1:]
	}
	basename = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, basename)
	basename = strings.Trim(basename, " .")
	if basename == "" {
		return fallback
	}
	return basename
}

func (b *BaseChannel) deriveStorageKey(part *ContentPart, mimeType string) string {
	ts := time.Now().Unix()
	rawURL := part.URL
	sum := md5.Sum([]byte(rawURL))
	urlHash := hex.EncodeToString(sum[:])[:8]
	ext := mimeToExt(mimeType, ".bin")
	if part.Kind == ContentTypeFile && part.Filename != "" {
		filename := b.SafeMediaFilename(part.Filename, "attachment")
		return fmt.Sprintf("%s/_outbound/%d_%s", b.channelType, ts, filename)
	}
	return fmt.Sprintf("%s/_outbound/%d_%s%s", b.channelType, ts, urlHash, ext)
}

// resolveMediaFilename generates a filename for a media part (mirrors the
// Python static method).
func resolveMediaFilename(part *ContentPart, index int, timestamp int64) string {
	md5Hash8 := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])[:8]
	}
	if part.Kind == ContentTypeFile && part.Filename != "" {
		return fmt.Sprintf("%d_%d_%s", timestamp, index, sanitizeForStorage(part.Filename))
	}
	if part.Kind == ContentTypeImage && part.URL != "" {
		ext := mimeToExt(part.MimeType, ".png")
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), ext)
	}
	if part.Kind == ContentTypeVideo {
		ext := mimeToExt(part.MimeType, ".mp4")
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), ext)
	}
	if part.Kind == ContentTypeAudio {
		ext := mimeToExt(part.MimeType, ".mp3")
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), ext)
	}
	source := part.URL
	if source == "" {
		source = fmt.Sprintf("%d", index)
	}
	return fmt.Sprintf("%d_%s.bin", timestamp, md5Hash8(source))
}

var controlCharsRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func sanitizeForStorage(filename string) string {
	if idx := strings.LastIndex(filename, "/"); idx >= 0 {
		filename = filename[idx+1:]
	}
	filename = controlCharsRe.ReplaceAllString(filename, "_")
	filename = strings.Trim(filename, " .")
	if filename == "" {
		return "attachment"
	}
	return filename
}

// MediaLabel gets a human-readable label for a media type.
func (b *BaseChannel) MediaLabel(part ContentPart) string {
	switch part.Kind {
	case ContentTypeImage:
		return "Image"
	case ContentTypeVideo:
		return "Video"
	case ContentTypeAudio:
		return "Audio"
	case ContentTypeFile:
		if part.Filename != "" {
			return "File: " + part.Filename
		}
		return "File"
	}
	return "Attachment"
}

// ---------------------------------------------------------------------------
// Subject Registry (in-memory)
// ---------------------------------------------------------------------------

// ListSubjects lists all known subjects who have interacted with this channel.
func (b *BaseChannel) ListSubjects() []*ChannelSubject {
	b.subjectsMu.Lock()
	defer b.subjectsMu.Unlock()
	out := make([]*ChannelSubject, 0, len(b.knownSubjects))
	for _, s := range b.knownSubjects {
		out = append(out, s)
	}
	return out
}

// GetSubject gets a specific subject by platform subject ID.
func (b *BaseChannel) GetSubject(subjectID string) *ChannelSubject {
	b.subjectsMu.Lock()
	defer b.subjectsMu.Unlock()
	return b.knownSubjects[subjectID]
}

// SetOnNewSubject sets the callback fired when a new subject is first seen.
func (b *BaseChannel) SetOnNewSubject(callback func(*ChannelSubject)) { b.onNewSubject = callback }

// TrackSubject tracks a subject from an inbound message. Called automatically
// in HandleInbound.
//
// Uses channel_subject.subject_id as the routing handle (open_id for DMs,
// chat_id for group chats).
func (b *BaseChannel) TrackSubject(message *InboundMessage) {
	if message.ChannelSubject == nil {
		return
	}
	subjectID := message.ChannelSubject.SubjectID
	if subjectID == "" || subjectID == "unknown" {
		return
	}
	now := message.Timestamp
	if now == 0 {
		now = nowFloat()
	}
	meta := map[string]any{}
	for k, v := range message.Metadata {
		meta[k] = v
	}

	b.subjectsMu.Lock()
	defer b.subjectsMu.Unlock()
	if existing, ok := b.knownSubjects[subjectID]; ok {
		existing.LastSeen = now
		existing.Metadata = meta
		if message.ChannelSubject.DisplayName != "" {
			existing.DisplayName = message.ChannelSubject.DisplayName
		}
		if message.ChannelSubject.ChatType != "" {
			existing.ChatType = message.ChannelSubject.ChatType
		}
		return
	}
	subject := &ChannelSubject{
		SubjectID:   subjectID,
		FirstSeen:   now,
		LastSeen:    now,
		DisplayName: message.ChannelSubject.DisplayName,
		ChatType:    message.ChannelSubject.ChatType,
		Metadata:    meta,
	}
	b.knownSubjects[subjectID] = subject
	if b.onNewSubject != nil {
		callback := b.onNewSubject
		go func() {
			defer func() { _ = recover() }()
			callback(subject)
		}()
	}
}

// ---------------------------------------------------------------------------
// Message processing pipeline
// ---------------------------------------------------------------------------

// HandleInbound runs the shared inbound template around platform-specific
// delivery.
//
//  1. Parse raw payload → InboundMessage
//  2. Let the adapter normalize/decrypt platform-specific content
//  3. Apply media persistence and shared group-context policy
//  4. Process and deliver events using the channel's output strategy
//  5. Finalize the group-context lifecycle after a successful reply
func (b *BaseChannel) HandleInbound(ctx context.Context, rawPayload any) error {
	message, subject, cont, err := b.PrepareInbound(ctx, rawPayload)
	if err != nil {
		return err
	}
	if !cont {
		return nil
	}
	processingSucceeded := b.processInbound(ctx, message, subject)
	b.FinalizeInbound(message, processingSucceeded)
	return nil
}

// PrepareInbound parses, preprocesses, tracks the subject, persists media,
// and applies the group-context policy. cont=false means the message was
// intentionally observed but must not start an agent turn.
func (b *BaseChannel) PrepareInbound(ctx context.Context, rawPayload any) (*InboundMessage, *ChannelSubject, bool, error) {
	// Already-parsed messages (manager worker loop, interrupt fast-path)
	// pass through untouched; only raw platform payloads hit the adapter's
	// ParseInbound.
	var message *InboundMessage
	if msg, ok := rawPayload.(*InboundMessage); ok {
		message = msg
	} else {
		var err error
		message, err = b.self.ParseInbound(ctx, rawPayload)
		if err != nil {
			return nil, nil, false, err
		}
	}
	if pre, ok := b.self.(Preprocessor); ok {
		pre.PreprocessInbound(ctx, message)
	}

	b.TrackSubject(message)
	// Persist media before a passive group message is reduced into the
	// short-lived context buffer.
	if b.mediaBackend != nil && b.groupContext.ShouldPersistMedia(message) {
		b.PersistMedia(ctx, message)
	}

	prepared, cont := b.groupContext.Prepare(message)
	if !cont {
		return nil, nil, false, nil
	}
	message = prepared

	subjectID := ""
	if message.ChannelSubject != nil {
		subjectID = message.ChannelSubject.SubjectID
	}
	subject := b.GetSubject(subjectID)
	if subject == nil {
		now := message.Timestamp
		if now == 0 {
			now = nowFloat()
		}
		subject = &ChannelSubject{SubjectID: subjectID, FirstSeen: now, LastSeen: now}
	}
	message.ChannelSubject = subject
	return message, subject, true, nil
}

// FinalizeInbound commits one-shot group-context state after successful
// completion.
func (b *BaseChannel) FinalizeInbound(message *InboundMessage, processingSucceeded bool) {
	if processingSucceeded {
		b.groupContext.MarkReplied(message)
	}
}

// ProcessInboundDefault is the default buffered event-delivery strategy.
//
// Streaming or protocol-specialized adapters override it via the
// ProcessInboundOverride interface. BaseChannel deliberately does not define
// a method named ProcessInbound (it would be promoted onto every embedding
// channel and make the override type assertion recurse into itself).
func (b *BaseChannel) ProcessInboundDefault(ctx context.Context, message *InboundMessage, subject *ChannelSubject) bool {
	if ov, ok := b.self.(ProcessInboundOverride); ok {
		return ov.ProcessInbound(ctx, message, subject)
	}
	return b.processInboundDefault(ctx, message, subject)
}

func (b *BaseChannel) processInbound(ctx context.Context, message *InboundMessage, subject *ChannelSubject) bool {
	if ov, ok := b.self.(ProcessInboundOverride); ok {
		return ov.ProcessInbound(ctx, message, subject)
	}
	return b.processInboundDefault(ctx, message, subject)
}

func (b *BaseChannel) processInboundDefault(ctx context.Context, message *InboundMessage, subject *ChannelSubject) (processingSucceeded bool) {
	// Delta accumulation buffer for streaming
	var deltaBuffer []string
	// Thinking accumulation buffer (separate from content)
	var thinkingBuffer []string

	flushThinking := func() error {
		if len(thinkingBuffer) == 0 {
			return nil
		}
		thinkingText := strings.Join(thinkingBuffer, "")
		thinkingBuffer = nil
		formatted := ReplaceTemplate(b.constraints.ThinkingTemplate, "{content}", strings.TrimSpace(thinkingText))
		return b.RateLimitedSend(ctx, subject, formatted)
	}
	flushDeltas := func() error {
		if len(deltaBuffer) == 0 {
			return nil
		}
		fullText := strings.Join(deltaBuffer, "")
		deltaBuffer = nil
		return b.RateLimitedSend(ctx, subject, fullText)
	}

	// Reply timeout guard
	var timeoutGuard *ReplyTimeoutGuard
	if b.constraints.ReplyTimeout > 0 {
		timeoutGuard = NewReplyTimeoutGuard(b.constraints.ReplyTimeout, func(tctx context.Context) {
			if b.constraints.TimeoutStrategy == TimeoutStrategyPlaceholder {
				_ = b.RateLimitedSend(tctx, subject, b.constraints.PlaceholderText())
			}
		})
		timeoutGuard.Start()
	}

	// Typing keepalive
	var typingKeepalive *TypingKeepalive
	if b.constraints.TypingKeepaliveInterval > 0 {
		typingKeepalive = NewTypingKeepalive(b.constraints.TypingKeepaliveInterval, func(tctx context.Context) {
			if t, ok := b.self.(TypingSender); ok {
				t.SendTypingIndicator(tctx, subject)
			}
		})
		typingKeepalive.Start()
	}

	cleanup := func() {
		if timeoutGuard != nil {
			timeoutGuard.Cancel()
		}
		if typingKeepalive != nil {
			typingKeepalive.Stop()
		}
	}

	defer func() {
		cleanup()
		if r := recover(); r != nil {
			// The processor is host-provided; one failed message must not
			// stop the worker.
			logError("error processing message: channel=%s session=%s panic=%v", b.channelID, message.ChannelSessionID, r)
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			_ = flushDeltas()
			_ = b.RateLimitedSend(ctx, subject, "An error occurred while processing your message.")
			processingSucceeded = false
		}
	}()

	events := b.processor(ctx, message)
	for event := range events {
		if event == nil {
			continue
		}
		switch event.Type {
		case EventDelta:
			for _, part := range event.Content {
				if part.Kind == ContentTypeText && part.Text != "" {
					deltaBuffer = append(deltaBuffer, part.Text)
				}
			}
		case EventThinking:
			// Complete thinking block — format and send if show_thinking
			b.onThinking(ctx, subject, event)
		case EventThinkingDelta:
			if b.constraints.ShowThinking {
				for _, part := range event.Content {
					if part.Kind == ContentTypeText && part.Text != "" {
						thinkingBuffer = append(thinkingBuffer, part.Text)
					}
				}
			}
		case EventFlush:
			if err := flushThinking(); err != nil {
				logError("flush thinking failed: %v", err)
			}
			if err := flushDeltas(); err != nil {
				logError("flush deltas failed: %v", err)
			}
		case EventCompleted:
			// Cancel timeout guard (we're about to reply)
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			if err := flushThinking(); err != nil {
				logError("flush thinking failed: %v", err)
			}
			if err := flushDeltas(); err != nil {
				logError("flush deltas failed: %v", err)
			}
			processingSucceeded = true
			cleanup()
			return processingSucceeded
		case EventMessage:
			// Cancel timeout guard on first real message
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			if err := flushThinking(); err != nil {
				logError("flush thinking failed: %v", err)
			}
			if err := flushDeltas(); err != nil {
				logError("flush deltas failed: %v", err)
			}
			// Send complete message immediately
			b.deliverEvent(ctx, subject, event)
		default:
			// TYPING, TOOL_START, TOOL_END, ERROR
			b.deliverEvent(ctx, subject, event)
		}
	}

	// The event stream ended without COMPLETED — treated as a processing
	// failure path in Python (exception) or silent end. Mirror the flush of
	// anything accumulated, without an error message (Python only flushed on
	// exception; a bare generator end left buffers unflushed).
	cleanup()
	return processingSucceeded
}

// RateLimitedSend cleans output and dispatches via ReplyText.
//
// Acts as the internal helper used inside HandleInbound and the constraint
// hook points (thinking, tool hints, error). Cleaning is applied first so
// empty results short-circuit before consuming a rate-limit slot.
func (b *BaseChannel) RateLimitedSend(ctx context.Context, subject *ChannelSubject, text string) error {
	text = b.CleanOutput(text)
	if text == "" {
		return nil
	}
	return b.ReplyText(ctx, subject, text)
}

var (
	thinkBlockRe   = regexp.MustCompile(`(?s)<think>(.*?)</think>`)
	thinkUnclosedRe = regexp.MustCompile(`(?s)<think>(.*)$`)
	thinkStripRe   = regexp.MustCompile(`(?s)<think>.*?</think>`)
	thinkStripTailRe = regexp.MustCompile(`(?s)<think>.*$`)
)

// CleanOutput is the legacy fallback: strip/format <think> tags in raw model
// output.
//
// With proper MessageEventType usage (THINKING_DELTA) this is a no-op. It is
// retained for backwards compatibility with processors that emit raw
// <think>...</think> text inside DELTA events.
func (b *BaseChannel) CleanOutput(text string) string {
	if text == "" {
		return ""
	}

	if b.constraints.ShowThinking {
		// Format thinking blocks with template prefix
		text = thinkBlockRe.ReplaceAllStringFunc(text, func(match string) string {
			groups := thinkBlockRe.FindStringSubmatch(match)
			content := strings.TrimSpace(groups[1])
			if content == "" {
				return ""
			}
			formatted := ReplaceTemplate(b.constraints.ThinkingTemplate, "{content}", content)
			return formatted + "\n\n"
		})
		// Handle unclosed <think> at the end
		if loc := thinkUnclosedRe.FindStringSubmatchIndex(text); loc != nil {
			content := strings.TrimSpace(thinkUnclosedRe.FindStringSubmatch(text)[1])
			if content != "" {
				formatted := ReplaceTemplate(b.constraints.ThinkingTemplate, "{content}", content)
				text = text[:loc[0]] + formatted + "\n\n"
			} else {
				text = text[:loc[0]]
			}
		}
	} else {
		// Remove complete <think>...</think> blocks
		text = thinkStripRe.ReplaceAllString(text, "")
		// Remove unclosed <think> at the end (truncated by max_tokens)
		text = thinkStripTailRe.ReplaceAllString(text, "")
	}

	return strings.TrimSpace(text)
}

// SendTypingIndicatorDefault is the shared no-op typing default. Platforms
// that support typing indicators implement TypingSender (their own
// SendTypingIndicator); BaseChannel deliberately does not define a
// same-named method so the type assertion cannot recurse through embedding.
func (b *BaseChannel) SendTypingIndicatorDefault(_ context.Context, _ *ChannelSubject) {
}

// deliverEvent delivers a single MessageEvent to the subject.
//
// Events handled in the main loop (DELTA, THINKING_DELTA, FLUSH, COMPLETED)
// never reach here. This handles the remaining event types that need
// immediate delivery.
func (b *BaseChannel) deliverEvent(ctx context.Context, subject *ChannelSubject, event *MessageEvent) {
	switch event.Type {
	case EventCompleted, EventDelta, EventThinkingDelta, EventFlush:
		return // handled by the buffer logic
	case EventThinking:
		return // handled by onThinking in the main loop
	case EventError:
		errorText := event.Error
		if errorText == "" {
			errorText = "An unexpected error occurred."
		}
		_ = b.RateLimitedSend(ctx, subject, errorText)
		return
	case EventTyping:
		return
	case EventToolStart:
		b.onToolStart(ctx, subject, event)
		return
	case EventToolEnd:
		b.onToolEnd(ctx, subject, event)
		return
	}

	// EventMessage — deliver content
	if len(event.Content) == 0 {
		return
	}
	textParts := ExtractText(event.Content)
	mediaParts := ExtractMedia(event.Content)

	if textParts != "" {
		_ = b.RateLimitedSend(ctx, subject, textParts)
	}
	for _, media := range mediaParts {
		if err := b.ReplyMedia(ctx, subject, media); err != nil {
			logError("reply media failed: %v", err)
		}
	}
}

// OnThinking handles a THINKING event. Formats and sends thinking content
// when show_thinking=True.
func (b *BaseChannel) onThinking(ctx context.Context, subject *ChannelSubject, event *MessageEvent) {
	if !b.constraints.ShowThinking {
		return
	}
	for _, part := range event.Content {
		if part.Kind == ContentTypeText && part.Text != "" {
			formatted := ReplaceTemplate(b.constraints.ThinkingTemplate, "{content}", strings.TrimSpace(part.Text))
			_ = b.RateLimitedSend(ctx, subject, formatted)
		}
	}
}

// OnToolStart handles a TOOL_START event. Default: sends a formatted status
// message using tool_hint_template (if show_tool_hints is true).
func (b *BaseChannel) onToolStart(ctx context.Context, subject *ChannelSubject, event *MessageEvent) {
	if !b.constraints.ShowToolHints {
		return
	}
	text := ToolHintMessage(event.Metadata, b.constraints, "start")
	_ = b.RateLimitedSend(ctx, subject, text)
}

// OnToolEnd handles a TOOL_END event. Sends a brief completion notice.
// Does NOT send the tool's output/result to the subject.
func (b *BaseChannel) onToolEnd(ctx context.Context, subject *ChannelSubject, event *MessageEvent) {
	if !b.constraints.ShowToolHints {
		return
	}
	text := ToolHintMessage(event.Metadata, b.constraints, "end")
	_ = b.RateLimitedSend(ctx, subject, text)
}

// ---------------------------------------------------------------------------
// Batching hooks (optional override)
// ---------------------------------------------------------------------------

// GetDebounceKeyDefault returns the default debounce/session key:
// channel_subject.subject_id (per-user serialization). Channels may implement
// DebounceKeyer to override; BaseChannel deliberately avoids a same-named
// method so the type assertion cannot recurse through embedding.
func (b *BaseChannel) GetDebounceKeyDefault(message *InboundMessage) string {
	if message.ChannelSubject != nil {
		return message.ChannelSubject.SubjectID
	}
	return ""
}

func (b *BaseChannel) debounceKey(message *InboundMessage) string {
	if k, ok := b.self.(DebounceKeyer); ok {
		return k.GetDebounceKey(message)
	}
	return b.GetDebounceKeyDefault(message)
}

// ShouldBatchInboundDefault mirrors the Python default: manager-level
// batching may merge unless the message is group-context handled.
func (b *BaseChannel) ShouldBatchInboundDefault(message *InboundMessage) bool {
	return !b.groupContext.Handles(message)
}

func (b *BaseChannel) shouldBatchInbound(message *InboundMessage) bool {
	if bb, ok := b.self.(InboundBatcher); ok {
		return bb.ShouldBatchInbound(message)
	}
	return b.ShouldBatchInboundDefault(message)
}

// MergeInboundDefault merges messages by concatenating content and keeping
// the earliest timestamp.
func (b *BaseChannel) MergeInboundDefault(messages []*InboundMessage) *InboundMessage {
	merged, err := MergeMessages(messages)
	if err != nil {
		return messages[0]
	}
	return merged
}

func (b *BaseChannel) mergeInbound(messages []*InboundMessage) *InboundMessage {
	if bb, ok := b.self.(InboundBatcher); ok {
		return bb.MergeInbound(messages)
	}
	return b.MergeInboundDefault(messages)
}

// ---------------------------------------------------------------------------
// Internal: enqueue support (used by ChannelManager)
// ---------------------------------------------------------------------------

// SetEnqueueCallback sets the enqueue callback (called by ChannelManager).
func (b *BaseChannel) SetEnqueueCallback(callback func(payload any)) {
	b.enqueueCallback = callback
}

// Enqueue enqueues a payload for processing (delegates to manager).
func (b *BaseChannel) Enqueue(payload any) {
	if b.enqueueCallback != nil {
		b.enqueueCallback(payload)
	} else {
		logWarn("no enqueue callback set for channel %s", b.channelID)
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
