package core

import (
	"errors"
	"net/http"
	"strings"
)

// ProviderError represents a failure returned by an AI provider.
type ProviderError struct {
	Message string
	Code    string
	Status  int
	Headers http.Header // full HTTP response headers on >= 400
}

// Error returns the error message.
func (e *ProviderError) Error() string {
	return e.Message
}

// IsRetryable reports whether the error is likely transient and safe to retry.
func (e *ProviderError) IsRetryable() bool {
	if e.Status == 429 || e.Status == 408 || e.Status == 409 {
		return true
	}
	if e.Status >= 500 {
		return true
	}
	return false
}

// IsContextTooLong reports whether the error indicates the input exceeded the model's context window.
func (e *ProviderError) IsContextTooLong() bool {
	if e.Status == 413 {
		return true
	}
	if e.Status == 400 {
		msg := strings.ToLower(e.Message)
		return strings.Contains(msg, "context") ||
			strings.Contains(msg, "token") ||
			strings.Contains(msg, "length") ||
			strings.Contains(msg, "too long")
	}
	return false
}

// ErrNoObjectGenerated is returned when GenerateObject fails to extract a valid object from the model response.
var ErrNoObjectGenerated = errors.New("no object generated")
// ErrModelNotFound is returned when the requested model ID is not recognized by the provider.
var ErrModelNotFound = errors.New("model not found")
// ErrUnsupportedFeature is returned when the provider does not support the requested capability.
var ErrUnsupportedFeature = errors.New("unsupported feature")

// ErrIncompleteStream is returned when a streaming response ends without
// a finish_reason from the provider, indicating the stream was truncated.
var ErrIncompleteStream = errors.New("stream ended without finish reason")

// ErrNotImplemented is returned when a provider does not yet implement a method.
var ErrNotImplemented = errors.New("not implemented")
