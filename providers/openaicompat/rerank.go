package openaicompat

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

// openaiRerankRequest is the request body for OpenAI-compatible rerank APIs.
type openaiRerankRequest struct {
	Model string   `json:"model"`
	Query string   `json:"query"`
	Docs  []string `json:"documents"`
	TopN  int      `json:"top_n,omitempty"`
}

// openaiRerankResponse is the response body for OpenAI-compatible rerank APIs.
type openaiRerankResponse struct {
	Model   string `json:"model"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document,omitempty"`
	} `json:"results"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// cohereRerankRequest is the request body for Cohere v2 rerank API.
type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents,omitempty"`
	MaxChunksPerDoc int      `json:"max_chunks_per_doc,omitempty"`
}

// cohereRerankResponse is the response body for Cohere v2 rerank API.
type cohereRerankResponse struct {
	ID      string `json:"id"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document,omitempty"`
	} `json:"results"`
}

func (c *Client) resolveRerankFormat() RerankFormat {
	if c.RerankFormat != "" && c.RerankFormat != RerankFormatAuto {
		return c.RerankFormat
	}
	if c.RerankPath == "/v2/rerank" {
		return RerankFormatCohereV2
	}
	return RerankFormatOpenAICompatible
}

// CreateRerank sends a rerank request to the API.
func (c *Client) CreateRerank(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("rerank: query is required")
	}
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("rerank: documents are required")
	}

	format := c.resolveRerankFormat()

	var resp *rerank.RerankResponse
	var err error

	switch format {
	case RerankFormatCohereV2:
		resp, err = c.createRerankCohere(ctx, model, req)
	case RerankFormatJina:
		resp, err = c.createRerankJina(ctx, model, req)
	default:
		resp, err = c.createRerankOpenAI(ctx, model, req)
	}

	if err != nil {
		return nil, fmt.Errorf("create rerank: %w", err)
	}
	return resp, nil
}

func (c *Client) createRerankOpenAI(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	body := openaiRerankRequest{
		Model: model,
		Query: req.Query,
		Docs:  req.Documents,
		TopN:  req.TopN,
	}

	path := c.RerankPath
	if path == "" {
		path = "/v1/rerank"
	}

	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}

	resp, err := core.HttpClientCall[openaiRerankResponse](
		ctx,
		"POST",
		c.BaseURL+path,
		nil,
		body,
		c.Headers,
	)
	if err != nil {
		return nil, err
	}

	results := make([]rerank.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		doc := ""
		if r.Document != nil {
			doc = r.Document.Text
		}
		results[i] = rerank.RerankResult{
			Index:          r.Index,
			RelevanceScore: float32(r.RelevanceScore),
			Document:       doc,
		}
	}

	return &rerank.RerankResponse{
		Results: results,
		Usage: core.Usage{
			PromptTokens: resp.Usage.PromptTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	}, nil
}

func (c *Client) createRerankJina(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	// Jina uses the same request/response format as OpenAI-compatible.
	return c.createRerankOpenAI(ctx, model, req)
}

func (c *Client) createRerankCohere(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	body := cohereRerankRequest{
		Model:           model,
		Query:           req.Query,
		Documents:       req.Documents,
		TopN:            req.TopN,
		ReturnDocuments: req.ReturnDocuments,
		MaxChunksPerDoc: req.MaxChunksPerDoc,
	}

	path := c.RerankPath
	if path == "" {
		path = "/v2/rerank"
	}

	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}

	resp, err := core.HttpClientCall[cohereRerankResponse](
		ctx,
		"POST",
		c.BaseURL+path,
		nil,
		body,
		c.Headers,
	)
	if err != nil {
		return nil, err
	}

	results := make([]rerank.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		doc := ""
		if r.Document != nil {
			doc = r.Document.Text
		}
		results[i] = rerank.RerankResult{
			Index:          r.Index,
			RelevanceScore: float32(r.RelevanceScore),
			Document:       doc,
		}
	}

	return &rerank.RerankResponse{
		ID:      resp.ID,
		Results: results,
		Usage:   core.Usage{},
	}, nil
}
