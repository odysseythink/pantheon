package openaicompat

import (
	"net/http"
)

// Client is a generic OpenAI-compatible HTTP client.
type Client struct {
	BaseURL            string
	APIKey             string
	HTTPClient         *http.Client
	Headers            map[string]string
	ChatCompletionPath string // default empty means "/v1/chat/completions"
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
