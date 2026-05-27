package cohere

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// LanguageModel implements core.LanguageModel for the Cohere provider.
type LanguageModel struct {
	provider *Provider
	client   *Client
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// chatRequest is the request body for Cohere Chat API v2.
type chatRequest struct {
	Model    string `json:"model"`
	Message  string `json:"message"`
	Preamble string `json:"preamble,omitempty"`
}

// chatResponse is the response body for Cohere Chat API v2.
type chatResponse struct {
	Text string `json:"text"`
}

// Generate sends a chat request and returns the response.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	var preamble string
	var message string

	for _, msg := range req.Messages {
		switch msg.Role {
		case core.MESSAGE_ROLE_SYSTEM:
			if preamble != "" {
				preamble += "\n"
			}
			preamble += msg.Text()
		case core.MESSAGE_ROLE_USER:
			message = msg.Text()
		}
	}

	if req.SystemPrompt != "" {
		if preamble != "" {
			preamble = req.SystemPrompt + "\n" + preamble
		} else {
			preamble = req.SystemPrompt
		}
	}

	payload := chatRequest{
		Model:    m.model,
		Message:  message,
		Preamble: preamble,
	}

	url := m.client.BaseURL + "/v2/chat"
	headers := map[string]string{
		"Authorization": "Bearer " + m.client.APIKey,
		"Content-Type":  "application/json",
	}

	resp, err := core.HttpClientCallWithClient[chatResponse](m.client.HTTPClient, ctx, "POST", url, nil, payload, headers)
	if err != nil {
		return nil, err
	}

	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: core.NewTextContent(resp.Text),
		},
		Model: m.model,
	}, nil
}

// Stream returns an error because streaming is not yet implemented.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, fmt.Errorf("cohere: streaming not yet implemented")
}

// GenerateObject returns an error because generate object is not yet implemented.
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, fmt.Errorf("cohere: generate object not yet implemented")
}

// StreamObject generates a structured object via streaming.
func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
