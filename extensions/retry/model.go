package retry

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/odysseythink/ai/core"
	extErrors "github.com/odysseythink/ai/extensions/errors"
)

// Model wraps a core.LanguageModel with exponential backoff retry.
type Model struct {
	Inner      core.LanguageModel
	MaxRetries int
	BaseDelay  time.Duration
	Multiplier float64
}

func (m *Model) Provider() string { return m.Inner.Provider() }
func (m *Model) Model() string    { return m.Inner.Model() }

func (m *Model) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return retry(m, ctx, func() (*core.Response, error) {
		return m.Inner.Generate(ctx, req)
	})
}

func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return retry(m, ctx, func() (core.StreamResponse, error) {
		return m.Inner.Stream(ctx, req)
	})
}

func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return retry(m, ctx, func() (*core.ObjectResponse, error) {
		return m.Inner.GenerateObject(ctx, req)
	})
}

func (m *Model) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return retry(m, ctx, func() (core.ObjectStreamResponse, error) {
		return m.Inner.StreamObject(ctx, req)
	})
}

// retry executes fn with exponential backoff retry on retryable errors.
func retry[T any](m *Model, ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
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
		delay = time.Duration(float64(delay) * mult)
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
