// Package tracing provides OpenTelemetry-style span recording
// without the full OpenTelemetry Go SDK. It exists so the hermes
// gateway can emit trace data to a file or in-memory exporter for
// local debugging without adding proto / OTLP dependencies.
package tracing

import (
	"encoding/json"
	"time"
)

// Status represents the terminal state of a span.
type Status int

const (
	// StatusUnset is the initial (and default) state — no explicit
	// decision has been made about the span's outcome.
	StatusUnset Status = iota
	// StatusOK means the span completed successfully.
	StatusOK
	// StatusError means the span ended in an error.
	StatusError
)

// Attribute is a key/value pair attached to a span.
type Attribute struct {
	Key   string
	Value any // string, int64, float64, bool — encoders should handle basic types
}

// String builds a string-valued Attribute.
func String(key, value string) Attribute { return Attribute{Key: key, Value: value} }

// Int64 builds an int-valued Attribute.
func Int64(key string, value int64) Attribute { return Attribute{Key: key, Value: value} }

// Float64 builds a float-valued Attribute.
func Float64(key string, value float64) Attribute { return Attribute{Key: key, Value: value} }

// Bool builds a bool-valued Attribute.
func Bool(key string, value bool) Attribute { return Attribute{Key: key, Value: value} }

// Event is a timestamped annotation attached to a span.
type Event struct {
	Name       string
	Time       time.Time
	Attributes []Attribute
}

// Span records the lifetime and metadata of one traced operation.
// Populated by Tracer.Start and finalized by Tracer.End.
type Span struct {
	TraceID      TraceID
	SpanID       SpanID
	ParentSpanID SpanID // zero if this is a root span
	Name         string
	StartTime    time.Time
	EndTime      time.Time
	Status       Status
	StatusMsg    string
	Attributes   []Attribute
	Events       []Event
}

// Duration is End - Start. Zero if End hasn't been called yet.
func (s *Span) Duration() time.Duration {
	if s.EndTime.IsZero() {
		return 0
	}
	return s.EndTime.Sub(s.StartTime)
}

// SetAttribute appends an attribute to the span. Safe only before
// the span is handed to an exporter; tracers avoid the race by
// calling it from the owning goroutine.
func (s *Span) SetAttribute(attr Attribute) {
	s.Attributes = append(s.Attributes, attr)
}

// AddEvent records a timestamped annotation on the span.
func (s *Span) AddEvent(name string, attrs ...Attribute) {
	s.Events = append(s.Events, Event{
		Name:       name,
		Time:       time.Now().UTC(),
		Attributes: attrs,
	})
}

// SetStatus records the terminal state of a span. Call before End.
func (s *Span) SetStatus(status Status, msg string) {
	s.Status = status
	s.StatusMsg = msg
}

// MarshalJSON serializes the span to a compact, human-readable
// JSON envelope suitable for the JSON-lines exporter.
func (s *Span) MarshalJSON() ([]byte, error) {
	type jsonAttr struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	type jsonEvent struct {
		Name       string     `json:"name"`
		Time       time.Time  `json:"time"`
		Attributes []jsonAttr `json:"attributes,omitempty"`
	}
	mapAttrs := func(src []Attribute) []jsonAttr {
		out := make([]jsonAttr, 0, len(src))
		for _, a := range src {
			out = append(out, jsonAttr{Key: a.Key, Value: a.Value})
		}
		return out
	}
	events := make([]jsonEvent, 0, len(s.Events))
	for _, e := range s.Events {
		events = append(events, jsonEvent{
			Name: e.Name, Time: e.Time, Attributes: mapAttrs(e.Attributes),
		})
	}
	return json.Marshal(struct {
		TraceID      string        `json:"trace_id"`
		SpanID       string        `json:"span_id"`
		ParentSpanID string        `json:"parent_span_id,omitempty"`
		Name         string        `json:"name"`
		StartTime    time.Time     `json:"start_time"`
		EndTime      time.Time     `json:"end_time"`
		DurationNS   int64         `json:"duration_ns"`
		Status       string        `json:"status"`
		StatusMsg    string        `json:"status_msg,omitempty"`
		Attributes   []jsonAttr    `json:"attributes,omitempty"`
		Events       []jsonEvent   `json:"events,omitempty"`
	}{
		TraceID:      s.TraceID.String(),
		SpanID:       s.SpanID.String(),
		ParentSpanID: nonZeroSpanID(s.ParentSpanID),
		Name:         s.Name,
		StartTime:    s.StartTime,
		EndTime:      s.EndTime,
		DurationNS:   s.Duration().Nanoseconds(),
		Status:       statusString(s.Status),
		StatusMsg:    s.StatusMsg,
		Attributes:   mapAttrs(s.Attributes),
		Events:       events,
	})
}

func nonZeroSpanID(id SpanID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
}

func statusString(s Status) string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}
