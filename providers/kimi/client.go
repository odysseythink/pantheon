package kimi

import (
	"net/http"
	"os"
	"runtime"
)

const defaultBaseURL = "https://api.kimi.com/coding/v1/"

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

func (c *Client) getHeaders(contentType ...string) map[string]string {
	if len(c.Headers) == 0 {
		c.Headers = map[string]string{}
	}
	if len(contentType) > 0 {
		c.Headers["Content-Type"] = contentType[0]
	} else {
		c.Headers["Content-Type"] = "application/json"
	}
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}
	// Kimi Code API (api.kimi.com) requires coding-agent identification headers.
	if isKimiCode(c.BaseURL) {
		c.Headers["User-Agent"] = "KimiCLI/1.0.0"
		c.Headers["X-Msh-Platform"] = "kimi_cli"
		c.Headers["X-Msh-Version"] = "1.0.0"
		c.Headers["X-Msh-Device-Name"] = "localhost"
		c.Headers["X-Msh-Device-Model"] = runtime.GOOS + " " + runtime.GOARCH
		c.Headers["X-Msh-Os-Version"] = runtime.GOOS
		c.Headers["X-Msh-Device-Id"] = "pantheon-test-device"
	}
	return c.Headers
}

func isKimiCode(baseURL string) bool {
	return baseURL == "https://api.kimi.com/coding/v1" ||
		baseURL == "https://api.kimi.com/coding/v1/"
}
