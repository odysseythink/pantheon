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
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		resp, err := m.Inner.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		stream, err := m.Inner.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		resp, err := m.Inner.GenerateObject(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		stream, err := m.Inner.StreamObject(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
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
	if mult <= 1 {
		mult = 2.0
	}

	delay := base
	for i := 0; i < attempt; i++ {
		delay = time.Duration(float64(delay) * mult)
	}
	// Add jitter: ±25%
	jitter := time.Duration(rand.Float64()*0.5*float64(delay)) - time.Duration(0.25*float64(delay))
	delay += jitter

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
