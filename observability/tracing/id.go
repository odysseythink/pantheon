package tracing

import (
	"crypto/rand"
	"encoding/hex"
	"io"
)

// TraceID is a 16-byte random identifier shared by every span in a
// single trace. It is hex-encoded (32 chars) when serialized.
type TraceID [16]byte

// SpanID is an 8-byte random identifier unique to a single span.
// Hex-encoded (16 chars) when serialized.
type SpanID [8]byte

// NewTraceID returns a random, non-zero trace id.
func NewTraceID() TraceID {
	var id TraceID
	_, _ = io.ReadFull(rand.Reader, id[:])
	return id
}

// NewSpanID returns a random, non-zero span id.
func NewSpanID() SpanID {
	var id SpanID
	_, _ = io.ReadFull(rand.Reader, id[:])
	return id
}

// String hex-encodes the TraceID.
func (t TraceID) String() string { return hex.EncodeToString(t[:]) }

// String hex-encodes the SpanID.
func (s SpanID) String() string { return hex.EncodeToString(s[:]) }

// IsZero reports whether the id is all-zero (i.e. unset).
func (t TraceID) IsZero() bool {
	for _, b := range t {
		if b != 0 {
			return false
		}
	}
	return true
}

// IsZero reports whether the span id is all-zero.
func (s SpanID) IsZero() bool {
	for _, b := range s {
		if b != 0 {
			return false
		}
	}
	return true
}
