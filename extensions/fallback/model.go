package fallback

import (
	"context"

	"github.com/odysseythink/ai/core"
)

// Model tries multiple LanguageModel candidates in order until one succeeds.
type Model struct {
	Candidates []core.LanguageModel
}

func (m *Model) Provider() string {
	if len(m.Candidates) > 0 {
		return m.Candidates[0].Provider()
	}
	return "fallback"
}

func (m *Model) Model() string {
	if len(m.Candidates) > 0 {
		return m.Candidates[0].Model()
	}
	return ""
}

func (m *Model) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return tryCandidates(m.Candidates, func(c core.LanguageModel) (*core.Response, error) {
		return c.Generate(ctx, req)
	})
}

func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return tryCandidates(m.Candidates, func(c core.LanguageModel) (core.StreamResponse, error) {
		return c.Stream(ctx, req)
	})
}

func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return tryCandidates(m.Candidates, func(c core.LanguageModel) (*core.ObjectResponse, error) {
		return c.GenerateObject(ctx, req)
	})
}

func (m *Model) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return tryCandidates(m.Candidates, func(c core.LanguageModel) (core.ObjectStreamResponse, error) {
		return c.StreamObject(ctx, req)
	})
}

// tryCandidates iterates over candidates and returns the first successful result.
func tryCandidates[T any](candidates []core.LanguageModel, fn func(core.LanguageModel) (T, error)) (T, error) {
	var zero T
	var lastErr error
	for _, candidate := range candidates {
		result, err := fn(candidate)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return zero, lastErr
}
