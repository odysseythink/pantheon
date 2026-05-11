package errors

import (
	"context"
	"errors"
	"io"

	"github.com/odysseythink/ai/core"
)

// Kind categorizes the nature of an error.
type Kind string

const (
	// KindRateLimit indicates the provider rejected the request due to rate limiting.
	KindRateLimit Kind = "rate_limit"
	// KindAuth indicates an authentication or authorization failure.
	KindAuth Kind = "auth"
	// KindTimeout indicates a timeout or cancellation.
	KindTimeout Kind = "timeout"
	// KindServerError indicates an internal server error from the provider.
	KindServerError Kind = "server_error"
	// KindContextTooLong indicates the prompt exceeded the model's context window.
	KindContextTooLong Kind = "context_too_long"
	// KindInvalidRequest indicates a malformed or invalid request.
	KindInvalidRequest Kind = "invalid_request"
	// KindUnknown indicates an unrecognized error type.
	KindUnknown Kind = "unknown"
)

// Classification describes an error's kind and whether retrying might succeed.
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
		if pe.Status >= 500 {
			return Classification{Kind: KindServerError, Retryable: true}
		}
		return Classification{Kind: KindUnknown, Retryable: false}
	}
}
