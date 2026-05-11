package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/odysseythink/ai/core"
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

func (c *Client) doJSON(ctx context.Context, method, path string, body, dst any) error {
	url := c.BaseURL + path
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		return &core.ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
