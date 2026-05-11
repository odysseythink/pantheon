package errors

import (
	"context"
	"errors"
	"io"

	"github.com/odysseythink/ai/core"
)

type Kind string

const (
	KindRateLimit      Kind = "rate_limit"
	KindAuth           Kind = "auth"
	KindTimeout        Kind = "timeout"
	KindServerError    Kind = "server_error"
	KindContextTooLong Kind = "context_too_long"
	KindInvalidRequest Kind = "invalid_request"
	KindUnknown        Kind = "unknown"
)

type Classification struct {
	Kind      Kind
	Retryable bool
}

// Classify determines the error kind and whether a retry might succeed.
func Classify(err error) Classification {
	if err == nil {
		return Classification{Kind: KindUnknown, Retryable: false}
	}

	// Context cancellation is always non-retryable.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Classification{Kind: KindTimeout, Retryable: false}
	}

	// Unexpected EOF during streaming is retryable.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return Classification{Kind: KindServerError, Retryable: true}
	}

	// Provider errors are classified by HTTP status.
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		return classifyProviderError(pe)
	}

	return Classification{Kind: KindUnknown, Retryable: false}
}

func classifyProviderError(pe *core.ProviderError) Classification {
	switch pe.Status {
	case 429:
		return Classification{Kind: KindRateLimit, Retryable: true}
	case 408:
		return Classification{Kind: KindTimeout, Retryable: true}
	case 500, 502, 503, 504:
		return Classification{Kind: KindServerError, Retryable: true}
	case 401, 403:
		return Classification{Kind: KindAuth, Retryable: false}
	case 413:
		return Classification{Kind: KindContextTooLong, Retryable: false}
	case 400:
		if pe.IsContextTooLong() {
			return Classification{Kind: KindContextTooLong, Retryable: false}
		}
		return Classification{Kind: KindInvalidRequest, Retryable: false}
	case 409:
		return Classification{Kind: KindServerError, Retryable: true}
	default:
		return Classification{Kind: KindUnknown, Retryable: false}
	}
}
