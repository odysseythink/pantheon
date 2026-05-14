package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewTraceIDAndSpanIDUnique(t *testing.T) {
	a := NewTraceID()
	b := NewTraceID()
	if a == b {
		t.Error("expected distinct trace ids")
	}
	if a.IsZero() || b.IsZero() {
		t.Error("expected non-zero trace ids")
	}
	s1 := NewSpanID()
	s2 := NewSpanID()
	if s1 == s2 {
		t.Error("expected distinct span ids")
	}
	if s1.IsZero() || s2.IsZero() {
		t.Error("expected non-zero span ids")
	}
}

func TestTracerStartEndRecordsDuration(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "outer")
	time.Sleep(2 * time.Millisecond)
	tr.End(span)

	spans := exp.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "outer" {
		t.Errorf("name = %q", spans[0].Name)
	}
	if spans[0].Duration() <= 0 {
		t.Errorf("expected positive duration, got %s", spans[0].Duration())
	}
	if spans[0].Status != StatusOK {
		t.Errorf("status = %d, want OK", spans[0].Status)
	}
}

func TestTracerNestedSpansShareTraceAndParent(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	ctx, outer := tr.Start(context.Background(), "outer")
	_, inner := tr.Start(ctx, "inner")
	tr.End(inner)
	tr.End(outer)

	spans := exp.Spans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	// MemoryExporter records in End order: inner first, then outer.
	gotInner, gotOuter := spans[0], spans[1]
	if gotInner.TraceID != gotOuter.TraceID {
		t.Errorf("trace ids differ: inner=%s outer=%s", gotInner.TraceID, gotOuter.TraceID)
	}
	if gotInner.ParentSpanID != gotOuter.SpanID {
		t.Errorf("inner parent span id = %s, want %s", gotInner.ParentSpanID, gotOuter.SpanID)
	}
	if !gotOuter.ParentSpanID.IsZero() {
		t.Errorf("outer parent should be zero, got %s", gotOuter.ParentSpanID)
	}
}

func TestSetStatusError(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "op")
	span.SetStatus(StatusError, "boom")
	tr.End(span)
	if exp.Spans()[0].Status != StatusError {
		t.Error("expected error status")
	}
	if exp.Spans()[0].StatusMsg != "boom" {
		t.Error("expected status msg")
	}
}

func TestJSONLinesExporter(t *testing.T) {
	var buf bytes.Buffer
	exp := NewJSONLinesExporter(&buf)
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "op", String("platform", "fake"))
	tr.End(span)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if decoded["name"] != "op" {
		t.Errorf("name = %v", decoded["name"])
	}
	if decoded["trace_id"] == "" || decoded["trace_id"] == nil {
		t.Error("missing trace_id")
	}
	attrs, _ := decoded["attributes"].([]any)
	if len(attrs) != 1 {
		t.Errorf("expected 1 attribute, got %d", len(attrs))
	}
}

func TestNilTracerIsNoop(t *testing.T) {
	var tr *Tracer
	ctx, span := tr.Start(context.Background(), "op")
	if span != nil {
		t.Error("expected nil span from nil tracer")
	}
	if ctx == nil {
		t.Error("expected original ctx")
	}
	tr.End(span) // must not panic
}

func TestSpanSetAttribute(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "attr-test")
	span.SetAttribute(String("key1", "val1"))
	span.SetAttribute(Int64("key2", 42))
	span.SetAttribute(Bool("key3", true))
	tr.End(span)

	spans := exp.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	attrs := spans[0].Attributes
	if len(attrs) != 3 {
		t.Fatalf("expected 3 attrs, got %d", len(attrs))
	}
	if attrs[0].Key != "key1" || attrs[0].Value != "val1" {
		t.Errorf("attr[0] = %+v", attrs[0])
	}
	if attrs[1].Key != "key2" || attrs[1].Value != int64(42) {
		t.Errorf("attr[1] = %+v", attrs[1])
	}
	if attrs[2].Key != "key3" || attrs[2].Value != true {
		t.Errorf("attr[2] = %+v", attrs[2])
	}
}

func TestSpanAddEvent(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "event-test")
	span.AddEvent("cache_miss", String("key", "foo"))
	tr.End(span)

	spans := exp.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if len(spans[0].Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(spans[0].Events))
	}
	if spans[0].Events[0].Name != "cache_miss" {
		t.Errorf("event name = %q", spans[0].Events[0].Name)
	}
	if len(spans[0].Events[0].Attributes) != 1 {
		t.Fatalf("expected 1 event attr, got %d", len(spans[0].Events[0].Attributes))
	}
	if spans[0].Events[0].Attributes[0].Key != "key" || spans[0].Events[0].Attributes[0].Value != "foo" {
		t.Errorf("event attr = %+v", spans[0].Events[0].Attributes[0])
	}
}

func TestStartAttributes(t *testing.T) {
	exp := NewMemoryExporter()
	tr := NewTracer(exp)
	_, span := tr.Start(context.Background(), "with-attrs", String("platform", "test"), Int64("pid", 123))
	tr.End(span)

	spans := exp.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if len(spans[0].Attributes) != 2 {
		t.Fatalf("expected 2 attrs, got %d", len(spans[0].Attributes))
	}
	if spans[0].Attributes[0].Key != "platform" || spans[0].Attributes[0].Value != "test" {
		t.Errorf("attr[0] = %+v", spans[0].Attributes[0])
	}
}

func TestSpanDurationUnset(t *testing.T) {
	s := &Span{}
	if s.Duration() != 0 {
		t.Errorf("unset duration = %v, want 0", s.Duration())
	}
}
