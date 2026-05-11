package errors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/odysseythink/ai/core"
)

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantKind  Kind
		wantRetry bool
	}{
		{"rate limit 429", &core.ProviderError{Status: 429}, KindRateLimit, true},
		{"server error 500", &core.ProviderError{Status: 500}, KindServerError, true},
		{"server error 502", &core.ProviderError{Status: 502}, KindServerError, true},
		{"auth 401", &core.ProviderError{Status: 401}, KindAuth, false},
		{"auth 403", &core.ProviderError{Status: 403}, KindAuth, false},
		{"invalid request 400", &core.ProviderError{Status: 400}, KindInvalidRequest, false},
		{"context too long 413", &core.ProviderError{Status: 413}, KindContextTooLong, false},
		{"timeout 408", &core.ProviderError{Status: 408}, KindTimeout, true},
		{"context canceled", context.Canceled, KindTimeout, false},
		{"deadline exceeded", context.DeadlineExceeded, KindTimeout, false},
		{"unexpected EOF", io.ErrUnexpectedEOF, KindServerError, true},
		{"unknown error", errors.New("something else"), KindUnknown, false},
		{"nil error", nil, KindUnknown, false},
		{"conflict 409", &core.ProviderError{Status: 409}, KindServerError, true},
		{"wrapped rate limit", fmt.Errorf("upstream: %w", &core.ProviderError{Status: 429}), KindRateLimit, true},
		{"other 5xx 501", &core.ProviderError{Status: 501}, KindServerError, true},
		{"other 5xx 505", &core.ProviderError{Status: 505}, KindServerError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Classify(tt.err)
			if c.Kind != tt.wantKind {
				t.Errorf("kind: got %q, want %q", c.Kind, tt.wantKind)
			}
			if c.Retryable != tt.wantRetry {
				t.Errorf("retryable: got %v, want %v", c.Retryable, tt.wantRetry)
			}
		})
	}
}

func TestClassifyContextTooLongMessage(t *testing.T) {
	err := &core.ProviderError{Status: 400, Message: "context length exceeded"}
	c := Classify(err)
	if c.Kind != KindContextTooLong {
		t.Errorf("kind: got %q, want %q", c.Kind, KindContextTooLong)
	}
	if c.Retryable {
		t.Error("context too long should not be retryable")
	}
}
