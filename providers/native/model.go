package native

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// LanguageModel implements core.LanguageModel for the native provider.
// All methods return errors because the native provider only supports embeddings.
type LanguageModel struct {
	provider *Provider
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// Generate returns an error because the native provider does not support chat completion.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, fmt.Errorf("native: chat completion not supported")
}

// Stream returns an error because the native provider does not support streaming.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, fmt.Errorf("native: streaming not supported")
}

// GenerateObject returns an error because the native provider does not support object generation.
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, fmt.Errorf("native: generate object not supported")
}
