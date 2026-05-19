package openaicompat

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// EmbeddingRequest is the request body for OpenAI-compatible embeddings.
type EmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbeddingData is a single embedding in the response.
type EmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

// EmbeddingResponse is the response body for OpenAI-compatible embeddings.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// CreateEmbeddings sends an embedding request to the OpenAI-compatible API.
func (c *Client) CreateEmbeddings(ctx context.Context, model string, texts []string) (*embed.EmbeddingResponse, error) {
	req := EmbeddingRequest{
		Model: model,
		Input: texts,
	}

	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}

	resp, err := core.HttpClientCall[EmbeddingResponse](
		ctx,
		"POST",
		c.BaseURL+"/v1/embeddings",
		nil,
		req,
		c.Headers,
	)
	if err != nil {
		return nil, fmt.Errorf("create embeddings: %w", err)
	}

	var embeddings [][]float32
	for _, data := range resp.Data {
		emb := make([]float32, len(data.Embedding))
		for i, v := range data.Embedding {
			emb[i] = float32(v)
		}
		embeddings = append(embeddings, emb)
	}

	return &embed.EmbeddingResponse{
		Embeddings: embeddings,
		Usage: core.Usage{
			PromptTokens: resp.Usage.PromptTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	}, nil
}
