package google

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

type client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func newClient(apiKey string) *client {
	return &client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		httpClient: http.DefaultClient,
	}
}

func (c *client) generateContent(ctx context.Context, model string, req *GenerateContentRequest) (*GenerateContentResponse, error) {
	url := fmt.Sprintf("%s/models/%s:generateContent", c.baseURL, model)
	query := map[string][]string{"key": {c.apiKey}}
	resp, err := core.HttpClientCallWithClient[GenerateContentResponse](
		c.httpClient, ctx, "POST", url, query, req,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
