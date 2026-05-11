package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/odysseythink/pantheon/core"
	extErrors "github.com/odysseythink/pantheon/extensions/errors"
)

// Model wraps a core.LanguageModel with exponential backoff retry.
type Model struct {
	// Inner is the LanguageModel to wrap.
	Inner core.LanguageModel
	// MaxRetries is the maximum number of retry attempts. Must be >= 0.
	MaxRetries int
	// BaseDelay is the initial delay between retries. Defaults to 1s.
	BaseDelay time.Duration
	// Multiplier is the exponential backoff multiplier. Defaults to 2.0.
	Multiplier float64
}

// Provider returns the provider name of the inner model.
func (m *Model) Provider() string { return m.Inner.Provider() }

// Model returns the model ID of the inner model.
func (m *Model) Model() string { return m.Inner.Model() }

// Generate retries the inner model on retryable errors.
func (m *Model) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return retry(m, ctx, func() (*core.Response, error) {
		return m.Inner.Generate(ctx, req)
	})
}

// Stream retries only if the initial Stream call fails. Mid-stream errors are not retried.
func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return retry(m, ctx, func() (core.StreamResponse, error) {
		return m.Inner.Stream(ctx, req)
	})
}

// GenerateObject retries the inner model on retryable errors.
func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return retry(m, ctx, func() (*core.ObjectResponse, error) {
		return m.Inner.GenerateObject(ctx, req)
	})
}


const maxDelay = 5 * time.Minute

// retry executes fn with exponential backoff retry on retryable errors.
func retry[T any](m *Model, ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if m.MaxRetries < 0 {
		return zero, errors.New("retry: MaxRetries cannot be negative")
	}
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return zero, err
		}
	}
	return zero, lastErr
}

func (m *Model) shouldRetry(err error) bool {
	c := extErrors.Classify(err)
	return c.Retryable
}

func (m *Model) sleep(ctx context.Context, attempt int) error {
	base := m.BaseDelay
	if base <= 0 {
		base = 1 * time.Second
	}
	mult := m.Multiplier
	if mult <= 0 {
		mult = 2.0
	}

	delay := base
	for i := 0; i < attempt; i++ {
		next := time.Duration(float64(delay) * mult)
		if next < 0 || next > maxDelay {
			next = maxDelay
		}
		delay = next
	}
	// Add jitter: delay * [0.75, 1.25]
	delay = time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
