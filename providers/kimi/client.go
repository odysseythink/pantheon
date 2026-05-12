package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"

	"github.com/odysseythink/pantheon/core"
)

const defaultBaseURL = "https://api.moonshot.ai/v1"

// Client is a Kimi API HTTP client.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Headers    map[string]string
}

// newClient creates a new Kimi client with the given API key.
func newClient(apiKey string) *Client {
	baseURL := defaultBaseURL
	if envURL := os.Getenv("KIMI_BASE_URL"); envURL != "" {
		baseURL = envURL
	}
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
	// Kimi Code API (api.kimi.com) requires coding-agent identification headers.
	if isKimiCode(c.BaseURL) {
		req.Header.Set("User-Agent", "KimiCLI/1.0.0")
		req.Header.Set("X-Msh-Platform", "kimi_cli")
		req.Header.Set("X-Msh-Version", "1.0.0")
		req.Header.Set("X-Msh-Device-Name", "localhost")
		req.Header.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
		req.Header.Set("X-Msh-Os-Version", runtime.GOOS)
		req.Header.Set("X-Msh-Device-Id", "pantheon-test-device")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
}

func isKimiCode(baseURL string) bool {
	return baseURL == "https://api.kimi.com/coding/v1" ||
		baseURL == "https://api.kimi.com/coding/v1/"
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

func (c *Client) uploadFile(ctx context.Context, path string, body io.Reader, contentType string, dst any) error {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	// Kimi Code API (api.kimi.com) requires coding-agent identification headers.
	if isKimiCode(c.BaseURL) {
		req.Header.Set("User-Agent", "KimiCLI/1.0.0")
		req.Header.Set("X-Msh-Platform", "kimi_cli")
		req.Header.Set("X-Msh-Version", "1.0.0")
		req.Header.Set("X-Msh-Device-Name", "localhost")
		req.Header.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
		req.Header.Set("X-Msh-Os-Version", runtime.GOOS)
		req.Header.Set("X-Msh-Device-Id", "pantheon-test-device")
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
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
