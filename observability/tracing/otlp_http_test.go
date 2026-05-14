package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"time"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestSpanToOTLP(t *testing.T) {
	traceID := NewTraceID()
	spanID := NewSpanID()
	parentID := NewSpanID()
	start := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	end := start.Add(500 * time.Millisecond)

	span := &Span{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentID,
		Name:         "test-op",
		StartTime:    start,
		EndTime:      end,
		Status:       StatusError,
		StatusMsg:    "boom",
		Attributes:   []Attribute{String("platform", "telegram"), Int64("retries", 3)},
		Events: []Event{{
			Name:       "retry",
			Time:       start.Add(100 * time.Millisecond),
			Attributes: []Attribute{String("reason", "timeout")},
		}},
	}

	otlpSpan := spanToOTLP(span)

	if string(otlpSpan.TraceId) != string(traceID[:]) {
		t.Errorf("trace id mismatch")
	}
	if string(otlpSpan.SpanId) != string(spanID[:]) {
		t.Errorf("span id mismatch")
	}
	if string(otlpSpan.ParentSpanId) != string(parentID[:]) {
		t.Errorf("parent span id mismatch")
	}
	if otlpSpan.Name != "test-op" {
		t.Errorf("name = %q", otlpSpan.Name)
	}
	if otlpSpan.StartTimeUnixNano != uint64(start.UnixNano()) {
		t.Errorf("start time mismatch")
	}
	if otlpSpan.EndTimeUnixNano != uint64(end.UnixNano()) {
		t.Errorf("end time mismatch")
	}
	if otlpSpan.Status.Code != tracepb.Status_STATUS_CODE_ERROR {
		t.Errorf("status = %v", otlpSpan.Status.Code)
	}
	if otlpSpan.Status.Message != "boom" {
		t.Errorf("status msg = %q", otlpSpan.Status.Message)
	}
	if len(otlpSpan.Attributes) != 2 {
		t.Fatalf("attributes len = %d", len(otlpSpan.Attributes))
	}
	if otlpSpan.Attributes[0].Key != "platform" {
		t.Errorf("attr[0] key = %q", otlpSpan.Attributes[0].Key)
	}
	strVal := otlpSpan.Attributes[0].Value.GetStringValue()
	if strVal != "telegram" {
		t.Errorf("attr[0] value = %q", strVal)
	}
	intVal := otlpSpan.Attributes[1].Value.GetIntValue()
	if intVal != 3 {
		t.Errorf("attr[1] value = %d", intVal)
	}
	if len(otlpSpan.Events) != 1 {
		t.Fatalf("events len = %d", len(otlpSpan.Events))
	}
	if otlpSpan.Events[0].Name != "retry" {
		t.Errorf("event name = %q", otlpSpan.Events[0].Name)
	}
}

func TestOTLPHTTPExporterSendsSpans(t *testing.T) {
	var received int32
	var lastBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("content-type = %s", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		lastBody = body
		atomic.AddInt32(&received, 1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	exp := NewOTLPHTTPExporter(OTLPHTTPConfig{
		Endpoint:      srv.URL,
		FlushInterval: 50 * time.Millisecond,
		BatchSize:     10,
	})
	exp.Export(&Span{
		TraceID: NewTraceID(), SpanID: NewSpanID(),
		Name: "test-op", StartTime: time.Now().UTC(),
		EndTime: time.Now().UTC().Add(100 * time.Millisecond), Status: StatusOK,
	})

	time.Sleep(200 * time.Millisecond)

	if atomic.LoadInt32(&received) < 1 {
		t.Fatalf("expected >= 1 request, got %d", received)
	}
	var req collectorpb.ExportTraceServiceRequest
	if err := proto.Unmarshal(lastBody, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	spans := req.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 1 || spans[0].Name != "test-op" {
		t.Errorf("unexpected spans: %v", spans)
	}
	exp.Shutdown(context.Background())
}

func TestOTLPHTTPExporterFlushesOnBatchSize(t *testing.T) {
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	exp := NewOTLPHTTPExporter(OTLPHTTPConfig{
		Endpoint:      srv.URL,
		FlushInterval: 10 * time.Second,
		BatchSize:     3,
	})
	for i := 0; i < 3; i++ {
		exp.Export(&Span{
			TraceID: NewTraceID(), SpanID: NewSpanID(),
			Name: "op", StartTime: time.Now().UTC(), EndTime: time.Now().UTC(), Status: StatusOK,
		})
	}

	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&received) < 1 {
		t.Errorf("expected flush on batch size, got %d requests", received)
	}
	exp.Shutdown(context.Background())
}

func TestOTLPHTTPExporterSendsHeaders(t *testing.T) {
	var headerOK int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer my-token" {
			atomic.AddInt32(&headerOK, 1)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	exp := NewOTLPHTTPExporter(OTLPHTTPConfig{
		Endpoint:      srv.URL,
		FlushInterval: 50 * time.Millisecond,
		BatchSize:     10,
		Headers:       map[string]string{"Authorization": "Bearer my-token"},
	})
	exp.Export(&Span{
		TraceID: NewTraceID(), SpanID: NewSpanID(),
		Name: "op", StartTime: time.Now().UTC(), EndTime: time.Now().UTC(),
	})

	time.Sleep(200 * time.Millisecond)
	exp.Shutdown(context.Background())
	if atomic.LoadInt32(&headerOK) < 1 {
		t.Error("custom header not received")
	}
}

func TestOTLPHTTPExporterWithTracer(t *testing.T) {
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req collectorpb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		spans := req.ResourceSpans[0].ScopeSpans[0].Spans
		for _, s := range spans {
			if s.Name == "parent" || s.Name == "child" {
				atomic.AddInt32(&received, 1)
			}
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	exp := NewOTLPHTTPExporter(OTLPHTTPConfig{
		Endpoint:      srv.URL,
		FlushInterval: 50 * time.Millisecond,
		BatchSize:     10,
	})
	tr := NewTracer(exp)

	ctx, parent := tr.Start(context.Background(), "parent", String("key", "val"))
	_, child := tr.Start(ctx, "child")
	tr.End(child)
	tr.End(parent)

	time.Sleep(200 * time.Millisecond)
	tr.Shutdown(context.Background())

	if atomic.LoadInt32(&received) < 2 {
		t.Errorf("expected 2 spans, got %d", received)
	}
}
