package native

import (
	"context"
	"errors"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// Provider implements core.Provider and embed.Provider for local embedding
// using the Cybertron library.
type Provider struct {
	modelDir  string
	modelName string
}

// New creates a new native embedding provider.
// modelDir is the base directory where models are stored.
// modelName is the model identifier (e.g. "sentence-transformers/all-MiniLM-L6-v2").
func New(modelDir, modelName string, opts ...Option) (core.Provider, error) {
	if modelDir == "" {
		return nil, errors.New("native: modelDir is required")
	}
	if modelName == "" {
		return nil, errors.New("native: modelName is required")
	}
	p := &Provider{
		modelDir:  modelDir,
		modelName: modelName,
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the native provider.
type Option func(*Provider)

// WithModelDir sets the models directory.
func WithModelDir(dir string) Option {
	return func(p *Provider) {
		p.modelDir = dir
	}
}

// WithModelName sets the model name.
func WithModelName(name string) Option {
	return func(p *Provider) {
		p.modelName = name
	}
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "native"
}

// Models returns a static list of known local embedding models.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return []core.Model{
		{ID: "sentence-transformers/all-MiniLM-L6-v2", Name: "all-MiniLM-L6-v2"},
		{ID: "sentence-transformers/LaBSE", Name: "LaBSE"},
		{ID: "Xenova/all-MiniLM-L6-v2", Name: "Xenova all-MiniLM-L6-v2"},
		{ID: "nomic-ai/nomic-embed-text-v1", Name: "nomic-embed-text-v1"},
		{ID: "intfloat/multilingual-e5-small", Name: "multilingual-e5-small"},
	}, nil
}

// LanguageModel returns an error because the native provider only supports embeddings.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, fmt.Errorf("native provider only supports embedding, not chat completion")
}

// EmbeddingModel creates a new native embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		modelID:  modelID,
	}, nil
}
