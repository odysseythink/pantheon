package voyage

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

const maxBatchSize = 128

// EmbeddingModel implements embed.EmbeddingModel for the Voyage AI provider.
type EmbeddingModel struct {
	client *Client
	model  string
}

// embedRequest is the request body for Voyage AI Embed API.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the response body for Voyage AI Embed API.
type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	var allEmbeddings [][]float32
	var totalUsage core.Usage

	url := m.client.BaseURL + "/embeddings"
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
			Model: m.model,
			Input: batch,
		}

		resp, err := core.HttpClientCallWithClient[embedResponse](m.client.HTTPClient, ctx, "POST", url, nil, payload, headers)
		if err != nil {
			return nil, err
		}

		for _, data := range resp.Data {
			emb := make([]float32, len(data.Embedding))
			for j, v := range data.Embedding {
				emb[j] = float32(v)
			}
			allEmbeddings = append(allEmbeddings, emb)
		}

		totalUsage.TotalTokens += resp.Usage.TotalTokens
	}

	return &embed.EmbeddingResponse{
		Embeddings: allEmbeddings,
		Usage:      totalUsage,
	}, nil
}
