package native

import (
	"context"
	"fmt"
	"sync"

	"github.com/nlpodyssey/cybertron/pkg/models/bert"
	"github.com/nlpodyssey/cybertron/pkg/tasks"
	"github.com/nlpodyssey/cybertron/pkg/tasks/textencoding"
	"github.com/nlpodyssey/spago/mat"
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// EmbeddingModel implements embed.EmbeddingModel for the native provider.
type EmbeddingModel struct {
	provider *Provider
	modelID  string

	once    sync.Once
	model   textencoding.Interface
	loadErr error
}

// Embed generates embeddings for the given texts using a local BERT model.
// Model loading happens lazily on the first call and errors are cached permanently
// for the lifetime of the EmbeddingModel (sync.Once semantics).
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	m.once.Do(func() {
		modelDir := m.provider.modelDir
		modelName := m.modelID
		if modelName == "" {
			modelName = m.provider.modelName
		}

		conf := &tasks.Config{
			ModelsDir:           modelDir,
			ModelName:           modelName,
			DownloadPolicy:      tasks.DownloadNever,
			ConversionPolicy:    tasks.ConvertNever,
			ConversionPrecision: tasks.F32,
		}

		m.model, m.loadErr = tasks.Load[textencoding.Interface](conf)
	})

	if m.loadErr != nil {
		return nil, fmt.Errorf("native: failed to load model: %w", m.loadErr)
	}

	embeddings := make([][]float32, 0, len(texts))
	for _, text := range texts {
		result, err := m.model.Encode(ctx, text, int(bert.MeanPooling))
		if err != nil {
			return nil, fmt.Errorf("native: failed to encode text: %w", err)
		}

		vec := mat.Data[float32](result.Vector)
		emb := make([]float32, len(vec))
		copy(emb, vec)
		embeddings = append(embeddings, emb)
	}

	return &embed.EmbeddingResponse{
		Embeddings: embeddings,
		Usage:      core.Usage{},
	}, nil
}
