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

// embedContentRequest is the request body for Gemini embedContent.
type embedContentRequest struct {
	Content struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"content"`
}

// embedContentResponse is the response body for Gemini embedContent.
type embedContentResponse struct {
	Embedding struct {
		Values []float64 `json:"values"`
	} `json:"embedding"`
}

// embedContent calls the Gemini embedContent API for a single text.
func (c *client) embedContent(ctx context.Context, model, text string) ([]float32, error) {
	req := embedContentRequest{}
	req.Content.Parts = append(req.Content.Parts, struct {
		Text string `json:"text"`
	}{Text: text})

	url := fmt.Sprintf("%s/models/%s:embedContent", c.baseURL, model)
	query := map[string][]string{"key": {c.apiKey}}
	resp, err := core.HttpClientCallWithClient[embedContentResponse](
		c.httpClient, ctx, "POST", url, query, req,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}

	emb := make([]float32, len(resp.Embedding.Values))
	for i, v := range resp.Embedding.Values {
		emb[i] = float32(v)
	}
	return emb, nil
}
