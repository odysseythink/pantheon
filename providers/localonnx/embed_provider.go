package localonnx

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// providerContextWindow mirrors the tokenizer's hard truncation limit
// (maxLen in tokenizer.go); BERT-family embedding models cap at 512 tokens.
const providerContextWindow = 512

// Provider adapts the local ONNX embedding runtime to pantheon's standard
// embed.Provider contract, so any pantheon consumer can use on-device
// embeddings through the same interface as the hosted providers.
//
// Root is the data root holding the runtime library (onnxruntime/) and the
// model cache (embedding_models/, fastembed-compatible layout). Models must
// be downloaded beforehand (see DownloadModel); Embed fails fast otherwise.
type Provider struct {
	Root string
}

var _ embed.Provider = (*Provider)(nil)

// Name implements core.Provider.
func (p *Provider) Name() string { return "localonnx" }

// Models implements core.Provider, exposing the ONNX catalog.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	catalog := Catalog()
	out := make([]core.Model, 0, len(catalog))
	for _, m := range catalog {
		out = append(out, core.Model{
			ID:            m.ID,
			Name:          m.ID,
			ContextWindow: providerContextWindow,
		})
	}
	return out, nil
}

// LanguageModel implements core.Provider; localonnx is embedding-only.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, fmt.Errorf("localonnx is embedding-only; %q is not a language model", modelID)
}

// EmbeddingModel implements embed.Provider. It validates the model id but
// does not load weights eagerly — loading happens on the first Embed call.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	if _, ok := Find(modelID); !ok {
		return nil, fmt.Errorf("model not in ONNX catalog: %s", modelID)
	}
	return &embeddingModel{root: p.Root, modelID: modelID}, nil
}

type embeddingModel struct {
	root    string
	modelID string
}

// Embed implements embed.EmbeddingModel over the local ONNX session.
func (m *embeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	vecs, err := EmbedTexts(m.root, m.modelID, texts)
	if err != nil {
		return nil, err
	}
	return &embed.EmbeddingResponse{Embeddings: vecs}, nil
}
