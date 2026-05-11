package core

import (
	"errors"
	"strings"
)

type ProviderError struct {
	Message string
	Code    string
	Status  int
}

func (e *ProviderError) Error() string {
	return e.Message
}

func (e *ProviderError) IsRetryable() bool {
	if e.Status == 429 || e.Status == 408 || e.Status == 409 {
		return true
	}
	if e.Status >= 500 {
		return true
	}
	return false
}

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

var ErrNoObjectGenerated = errors.New("no object generated")
var ErrModelNotFound = errors.New("model not found")
var ErrUnsupportedFeature = errors.New("unsupported feature")
