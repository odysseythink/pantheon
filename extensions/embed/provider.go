package embed

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Provider extends core.Provider with embedding capabilities.
type Provider interface {
	core.Provider
	EmbeddingModel(ctx context.Context, modelID string) (EmbeddingModel, error)
}

// EmbeddingModel generates vector embeddings for text.
type EmbeddingModel interface {
	Embed(ctx context.Context, texts []string) (*EmbeddingResponse, error)
}

// EmbeddingResponse holds embeddings and token usage.
type EmbeddingResponse struct {
	Embeddings [][]float32
	Usage      core.Usage
}
