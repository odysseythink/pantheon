package tracing

import (
	"context"
	"encoding/json"
	"io"
	"sync"
)

// Exporter receives finalized spans for persistence or further
// processing (file, in-memory buffer, OTLP, etc.). Implementations
// must be safe for concurrent use.
type Exporter interface {
	Export(span *Span)
	Shutdown(ctx context.Context) error
}

// NoopExporter discards every span it is handed.
type NoopExporter struct{}

func (NoopExporter) Export(*Span)                         {}
func (NoopExporter) Shutdown(context.Context) error       { return nil }

// MemoryExporter buffers spans in memory. Used by tests and for
// in-process introspection.
type MemoryExporter struct {
	mu    sync.Mutex
	spans []*Span
}

func NewMemoryExporter() *MemoryExporter {
	return &MemoryExporter{}
}

func (m *MemoryExporter) Export(s *Span) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = append(m.spans, s)
}

// Spans returns a snapshot of recorded spans.
func (m *MemoryExporter) Spans() []*Span {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Span, len(m.spans))
	copy(out, m.spans)
	return out
}

// Reset clears the buffer.
func (m *MemoryExporter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spans = nil
}

func (m *MemoryExporter) Shutdown(context.Context) error { return nil }

// JSONLinesExporter writes each span as one JSON object per line to
// the configured io.Writer. Writes are serialized by a mutex so the
// output remains well-formed under concurrent Export calls.
type JSONLinesExporter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLinesExporter builds an exporter that writes to w.
func NewJSONLinesExporter(w io.Writer) *JSONLinesExporter {
	return &JSONLinesExporter{w: w}
}

func (j *JSONLinesExporter) Export(s *Span) {
	buf, err := json.Marshal(s)
	if err != nil {
		return
	}
	buf = append(buf, '\n')
	j.mu.Lock()
	defer j.mu.Unlock()
	_, _ = j.w.Write(buf)
}

// Shutdown is a no-op for a simple stream writer; callers that need
// to flush the underlying writer should do so externally.
func (j *JSONLinesExporter) Shutdown(ctx context.Context) error { return nil }
