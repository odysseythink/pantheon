package cohere

import (
	"net/http"
)

const defaultBaseURL = "https://api.cohere.com"

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func newClient(apiKey string) *Client {
	return &Client{
		BaseURL:    defaultBaseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
}
