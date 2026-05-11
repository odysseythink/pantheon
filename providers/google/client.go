package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, model, c.apiKey)
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyData, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, &core.ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}

	var genResp GenerateContentResponse
	if err := json.Unmarshal(bodyData, &genResp); err != nil {
		return nil, err
	}
	return &genResp, nil
}
