package voyage

import (
	"net/http"
)

const defaultBaseURL = "https://api.voyageai.com/v1"

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
