package openaicompat

import (
	"net/http"
)

// RerankFormat defines the API format for rerank requests.
type RerankFormat string

const (
	RerankFormatAuto             RerankFormat = "auto"
	RerankFormatOpenAICompatible RerankFormat = "openai"
	RerankFormatCohereV2         RerankFormat = "cohere"
	RerankFormatJina             RerankFormat = "jina"
	// RerankFormatDashScope is Alibaba Cloud Model Studio's native rerank
	// protocol (gte-rerank-v2, qwen3-vl-rerank, ...): POST
	// {BaseURL}/api/v1/services/rerank/text-rerank/text-rerank with an
	// input/parameters-shaped body. Qwen's qwen3-rerank is NOT this format;
	// it is served by the OpenAI-compatible endpoint (use RerankPath
	// "/compatible-api/v1/reranks" with RerankFormatOpenAICompatible).
	RerankFormatDashScope RerankFormat = "dashscope"
)

// Client is a generic OpenAI-compatible HTTP client.
type Client struct {
	BaseURL            string
	APIKey             string
	HTTPClient         *http.Client
	Headers            map[string]string
	ChatCompletionPath string // default empty means "/v1/chat/completions"
	EmbeddingPath      string // default empty means "/v1/embeddings"
	RerankPath         string // default empty means "/v1/rerank"
	RerankFormat       RerankFormat
	// Hooks allow providers to customize the behavior of the client.
	Hooks Hooks
}

// NewClient creates a new OpenAI-compatible client for the given base URL and API key.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
		Headers:    make(map[string]string),
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
}
