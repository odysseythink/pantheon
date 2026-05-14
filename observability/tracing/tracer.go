package tracing

import (
	"context"
	"time"
)

// Tracer creates and ends spans against a single exporter.
// A nil *Tracer is valid: Start returns the original ctx and a nil
// span pointer, and End is a no-op. This lets callers use the
// tracing API unconditionally regardless of whether tracing is
// actually configured.
type Tracer struct {
	exporter Exporter
}

// NewTracer wraps an exporter in a Tracer. Pass NoopExporter to
// disable export without making the Tracer itself nil.
func NewTracer(exp Exporter) *Tracer {
	if exp == nil {
		exp = NoopExporter{}
	}
	return &Tracer{exporter: exp}
}

// Start creates a new span as a child of any span already in ctx.
// When there is no parent, a new trace id is generated.
func (t *Tracer) Start(ctx context.Context, name string, attrs ...Attribute) (context.Context, *Span) {
	if t == nil {
		return ctx, nil
	}
	span := &Span{
		SpanID:     NewSpanID(),
		Name:       name,
		StartTime:  time.Now().UTC(),
		Attributes: append([]Attribute{}, attrs...),
	}
	if parent := SpanFromContext(ctx); parent != nil {
		span.TraceID = parent.TraceID
		span.ParentSpanID = parent.SpanID
	} else {
		span.TraceID = NewTraceID()
	}
	ctx = ContextWithSpan(ctx, span)
	return ctx, span
}

// End finalizes the span and hands it to the exporter. Safe to call
// on a nil Tracer or nil span.
func (t *Tracer) End(span *Span) {
	if t == nil || span == nil {
		return
	}
	span.EndTime = time.Now().UTC()
	if span.Status == StatusUnset {
		span.Status = StatusOK
	}
	t.exporter.Export(span)
}

// Shutdown flushes the underlying exporter.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	return t.exporter.Shutdown(ctx)
}
