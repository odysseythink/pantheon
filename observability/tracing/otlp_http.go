package tracing

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/odysseythink/mlog"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// spanToOTLP converts an internal Span to an OTLP protobuf Span.
func spanToOTLP(s *Span) *tracepb.Span {
	otlp := &tracepb.Span{
		TraceId:           s.TraceID[:],
		SpanId:            s.SpanID[:],
		Name:              s.Name,
		StartTimeUnixNano: uint64(s.StartTime.UnixNano()),
		EndTimeUnixNano:   uint64(s.EndTime.UnixNano()),
		Attributes:        attributesToOTLP(s.Attributes),
		Events:            eventsToOTLP(s.Events),
		Status:            statusToOTLP(s.Status, s.StatusMsg),
	}
	if !s.ParentSpanID.IsZero() {
		otlp.ParentSpanId = s.ParentSpanID[:]
	}
	return otlp
}

func attributesToOTLP(attrs []Attribute) []*commonpb.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, len(attrs))
	for i, a := range attrs {
		out[i] = &commonpb.KeyValue{
			Key:   a.Key,
			Value: anyValueToOTLP(a.Value),
		}
	}
	return out
}

func anyValueToOTLP(v any) *commonpb.AnyValue {
	switch val := v.(type) {
	case string:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: val}}
	case int64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: val}}
	case float64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: val}}
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: val}}
	case int:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(val)}}
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: ""}}
	}
}

func eventsToOTLP(events []Event) []*tracepb.Span_Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]*tracepb.Span_Event, len(events))
	for i, e := range events {
		out[i] = &tracepb.Span_Event{
			TimeUnixNano: uint64(e.Time.UnixNano()),
			Name:         e.Name,
			Attributes:   attributesToOTLP(e.Attributes),
		}
	}
	return out
}

func statusToOTLP(s Status, msg string) *tracepb.Status {
	var code tracepb.Status_StatusCode
	switch s {
	case StatusOK:
		code = tracepb.Status_STATUS_CODE_OK
	case StatusError:
		code = tracepb.Status_STATUS_CODE_ERROR
	default:
		code = tracepb.Status_STATUS_CODE_UNSET
	}
	return &tracepb.Status{Code: code, Message: msg}
}

// OTLPHTTPConfig configures the OTLP/HTTP exporter.
type OTLPHTTPConfig struct {
	Endpoint      string            // base URL, e.g. "http://localhost:4318"
	Headers       map[string]string // sent with every request
	BatchSize     int               // max spans per flush (default 256)
	FlushInterval time.Duration     // how often to flush (default 5s)
}

// OTLPHTTPExporter exports spans to an OTLP/HTTP endpoint.
type OTLPHTTPExporter struct {
	cfg     OTLPHTTPConfig
	client  *http.Client
	mu      sync.Mutex
	buffer  []*Span
	done    chan struct{}
	stopped bool
}

func NewOTLPHTTPExporter(cfg OTLPHTTPConfig) *OTLPHTTPExporter {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 256
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}
	e := &OTLPHTTPExporter{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		buffer: make([]*Span, 0, cfg.BatchSize),
		done:   make(chan struct{}),
	}
	go e.flushLoop()
	return e
}

func (e *OTLPHTTPExporter) Export(s *Span) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return
	}
	e.buffer = append(e.buffer, s)
	if len(e.buffer) >= e.cfg.BatchSize {
		e.flushLocked()
	}
}

func (e *OTLPHTTPExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.flushLocked()
	e.mu.Unlock()
	close(e.done)
	return nil
}

func (e *OTLPHTTPExporter) flushLoop() {
	ticker := time.NewTicker(e.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.done:
			return
		case <-ticker.C:
			e.mu.Lock()
			e.flushLocked()
			e.mu.Unlock()
		}
	}
}

func (e *OTLPHTTPExporter) flushLocked() {
	if len(e.buffer) == 0 {
		return
	}
	batch := e.buffer
	e.buffer = make([]*Span, 0, e.cfg.BatchSize)

	otlpSpans := make([]*tracepb.Span, len(batch))
	for i, s := range batch {
		otlpSpans[i] = spanToOTLP(s)
	}

	req := &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource:   &resourcepb.Resource{},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: otlpSpans}},
		}},
	}

	go e.send(req)
}

func (e *OTLPHTTPExporter) send(req *collectorpb.ExportTraceServiceRequest) {
	body, err := proto.Marshal(req)
	if err != nil {
		mlog.Warning("otlp: marshal error", mlog.String("err", err.Error()))
		return
	}
	url := e.cfg.Endpoint + "/v1/traces"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		mlog.Warning("otlp: request error", mlog.String("err", err.Error()))
		return
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range e.cfg.Headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := e.client.Do(httpReq)
	if err != nil {
		mlog.Warning("otlp: send error", mlog.String("err", err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		mlog.Warning("otlp: transient error, spans dropped", mlog.Int("status", resp.StatusCode))
		return
	}
	if resp.StatusCode >= 300 {
		mlog.Warning("otlp: export failed", mlog.Int("status", resp.StatusCode))
	}
}
