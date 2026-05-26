package cohere

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

const maxBatchSize = 96

// EmbeddingModel implements embed.EmbeddingModel for the Cohere provider.
type EmbeddingModel struct {
	provider *Provider
	client   *Client
	model    string
}

// embedRequest is the request body for Cohere Embed API v2.
type embedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
}

// embedResponse is the response body for Cohere Embed API v2.
type embedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	var allEmbeddings [][]float32
	var totalUsage core.Usage

	inputType := "search_document"
	if len(texts) == 1 {
		inputType = "search_query"
	}

	url := m.client.BaseURL + "/v2/embed"
	headers := map[string]string{
		"Authorization": "Bearer " + m.client.APIKey,
		"Content-Type":  "application/json",
	}

	for i := 0; i < len(texts); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		payload := embedRequest{
			Texts:     batch,
			Model:     m.model,
			InputType: inputType,
		}

		resp, err := core.HttpClientCallWithClient[embedResponse](m.client.HTTPClient, ctx, "POST", url, nil, payload, headers)
		if err != nil {
			return nil, err
		}

		for _, emb := range resp.Embeddings {
			float32Emb := make([]float32, len(emb))
			for j, v := range emb {
				float32Emb[j] = float32(v)
			}
			allEmbeddings = append(allEmbeddings, float32Emb)
		}
	}

	return &embed.EmbeddingResponse{
		Embeddings: allEmbeddings,
		Usage:      totalUsage,
	}, nil
}
