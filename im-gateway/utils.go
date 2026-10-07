package imgateway

// Utility functions for content inspection, merging, and debouncing.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var mediaTypes = map[ContentType]bool{
	ContentTypeImage: true,
	ContentTypeVideo: true,
	ContentTypeAudio: true,
	ContentTypeFile:  true,
}

// ---------------------------------------------------------------------------
// Content inspection helpers
// ---------------------------------------------------------------------------

// HasText returns true if any part contains non-empty text.
func HasText(parts []ContentPart) bool {
	for _, p := range parts {
		if p.Kind == ContentTypeText && strings.TrimSpace(p.Text) != "" {
			return true
		}
	}
	return false
}

// HasMedia returns true if any part is a media type (image/video/audio/file).
func HasMedia(parts []ContentPart) bool {
	for _, p := range parts {
		if mediaTypes[p.Kind] {
			return true
		}
	}
	return false
}

// ExtractText concatenates all text content parts into a single string.
func ExtractText(parts []ContentPart) string {
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Kind == ContentTypeText && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// ExtractMedia extracts all media (non-text) content parts.
func ExtractMedia(parts []ContentPart) []ContentPart {
	out := make([]ContentPart, 0, len(parts))
	for _, p := range parts {
		if mediaTypes[p.Kind] {
			out = append(out, p)
		}
	}
	return out
}

// GetMediaURL gets the URL from a media content part, or "" for text.
func GetMediaURL(part ContentPart) string {
	if mediaTypes[part.Kind] {
		return part.URL
	}
	return ""
}

// ---------------------------------------------------------------------------
// Message merging
// ---------------------------------------------------------------------------

// MergeMessages merges multiple inbound messages (same session) into one.
//
// Concatenates content parts, uses the first message's metadata as base,
// keeps the earliest timestamp.
func MergeMessages(messages []*InboundMessage) (*InboundMessage, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("cannot merge empty message list")
	}
	if len(messages) == 1 {
		return messages[0], nil
	}

	first := messages[0]
	merged := first.Clone()
	mergedContent := make([]ContentPart, 0, 16)
	mergedMeta := make(map[string]any, len(first.Metadata)+4)
	for k, v := range first.Metadata {
		mergedMeta[k] = v
	}
	earliest := first.Timestamp

	for _, msg := range messages {
		mergedContent = append(mergedContent, msg.Content...)
		if msg.Timestamp < earliest {
			earliest = msg.Timestamp
		}
		// Merge metadata (later messages may add keys)
		for k, v := range msg.Metadata {
			if _, ok := mergedMeta[k]; !ok {
				mergedMeta[k] = v
			}
		}
	}

	merged.Content = mergedContent
	merged.Metadata = mergedMeta
	merged.Timestamp = earliest
	return merged, nil
}

// ---------------------------------------------------------------------------
// Number formatting helpers (Python str() compatible in common cases)
// ---------------------------------------------------------------------------

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func utoa(v uint64) string { return strconv.FormatUint(v, 10) }

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	return s
}

// ---------------------------------------------------------------------------
// Debouncer
// ---------------------------------------------------------------------------

// DebounceCallback receives all accumulated items for a key on flush.
type DebounceCallback func(ctx context.Context, key string, items []any)

// Debouncer collects items under a key and flushes after a delay.
//
// When items arrive for the same key within delay seconds, they are batched
// together. When the delay expires (or Flush is called explicitly), the
// callback receives all accumulated items.
type Debouncer struct {
	delay    time.Duration
	callback DebounceCallback

	mu      sync.Mutex
	buffers map[string][]any
	timers  map[string]*time.Timer
}

// NewDebouncer builds a debouncer with the given delay and flush callback.
func NewDebouncer(delaySeconds float64, callback DebounceCallback) *Debouncer {
	return &Debouncer{
		delay:    time.Duration(delaySeconds * float64(time.Second)),
		callback: callback,
		buffers:  map[string][]any{},
		timers:   map[string]*time.Timer{},
	}
}

// Add adds an item to the debounce buffer for the given key, restarting the
// delay timer.
func (d *Debouncer) Add(ctx context.Context, key string, item any) {
	d.mu.Lock()
	d.buffers[key] = append(d.buffers[key], item)
	if old := d.timers[key]; old != nil {
		old.Stop()
	}
	d.mu.Unlock()

	timer := time.AfterFunc(d.delay, func() {
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
		d.Flush(ctx, key)
	})
	d.mu.Lock()
	d.timers[key] = timer
	d.mu.Unlock()
}

// Flush immediately flushes all items for the given key.
func (d *Debouncer) Flush(ctx context.Context, key string) {
	d.mu.Lock()
	items := d.buffers[key]
	delete(d.buffers, key)
	if timer := d.timers[key]; timer != nil {
		timer.Stop()
		delete(d.timers, key)
	}
	d.mu.Unlock()
	if len(items) > 0 {
		func() {
			defer func() {
				// The callback is supplied by the host; isolate failures by
				// debounce key.
				_ = recover()
			}()
			d.callback(ctx, key, items)
		}()
	}
}

// FlushAll flushes all pending buffers immediately.
func (d *Debouncer) FlushAll(ctx context.Context) {
	d.mu.Lock()
	keys := make([]string, 0, len(d.buffers))
	for k := range d.buffers {
		keys = append(keys, k)
	}
	d.mu.Unlock()
	for _, key := range keys {
		d.Flush(ctx, key)
	}
}

// PendingKeys returns the list of keys with pending items.
func (d *Debouncer) PendingKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]string, 0, len(d.buffers))
	for k, v := range d.buffers {
		if len(v) > 0 {
			keys = append(keys, k)
		}
	}
	return keys
}
