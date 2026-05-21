package openaicompat

import (
	"context"
	"os"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

// integrationConfig holds configuration for live LLM integration tests,
// read from environment variables. If any required variable is missing,
// integration tests are skipped.
type integrationConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// integrationTestConfig reads integration test configuration from environment variables:
//   - OPENAICOMPAT_BASE_URL: the API base URL (e.g. https://api.openai.com)
//   - OPENAICOMPAT_API_KEY: the API key for authentication
//   - OPENAICOMPAT_MODEL: the model name (e.g. gpt-4o-mini)
//
// All three must be set for integration tests to run.
func integrationTestConfig(t *testing.T) *integrationConfig {
	t.Helper()
	baseURL := os.Getenv("OPENAICOMPAT_BASE_URL")
	apiKey := os.Getenv("OPENAICOMPAT_API_KEY")
	model := os.Getenv("OPENAICOMPAT_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("Skipping integration test: set OPENAICOMPAT_BASE_URL, OPENAICOMPAT_API_KEY, OPENAICOMPAT_MODEL to run")
	}
	return &integrationConfig{BaseURL: baseURL, APIKey: apiKey, Model: model}
}

func newIntegrationClient(t *testing.T, cfg *integrationConfig) *Client {
	t.Helper()
	c := NewClient(cfg.BaseURL, cfg.APIKey)
	return c
}

func TestIntegration_ChatCompletion(t *testing.T) {
	cfg := integrationTestConfig(t)
	c := newIntegrationClient(t, cfg)

	resp, err := c.ChatCompletion(context.Background(), cfg.Model, &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Say hello in one word."}}},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	if len(resp.Message.Content) == 0 {
		t.Fatal("expected at least one content part in response")
	}
	tp, ok := resp.Message.Content[0].(core.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", resp.Message.Content[0])
	}
	if tp.Text == "" {
		t.Error("expected non-empty text response")
	}
	t.Logf("Response: %s", tp.Text)
	t.Logf("Usage: prompt=%d completion=%d total=%d",
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
}

func TestIntegration_ChatCompletionStream(t *testing.T) {
	cfg := integrationTestConfig(t)
	c := newIntegrationClient(t, cfg)

	stream := c.ChatCompletionStream(context.Background(), cfg.Model, &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Count from 1 to 5."}}},
		},
	})

	var textDeltas []string
	var finishReason string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		}
	}

	fullText := ""
	for _, d := range textDeltas {
		fullText += d
	}
	if fullText == "" {
		t.Error("expected non-empty streamed text")
	}
	if finishReason == "" {
		t.Error("expected non-empty finish reason")
	}
	t.Logf("Streamed text: %s", fullText)
	t.Logf("Finish reason: %s", finishReason)
}

func TestIntegration_ChatCompletionWithTools(t *testing.T) {
	cfg := integrationTestConfig(t)
	c := newIntegrationClient(t, cfg)

	resp, err := c.ChatCompletion(context.Background(), cfg.Model, &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "What is the weather in New York?"}}},
		},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get the current weather for a city",
			Parameters: &core.Schema{
				Type: "object",
				Properties: map[string]*core.Schema{
					"city": {Type: "string", Description: "City name"},
				},
				Required: []string{"city"},
			},
		}},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeAuto},
	})
	if err != nil {
		t.Fatalf("ChatCompletion with tools error: %v", err)
	}

	// The model should respond with either a tool call or text — both are valid.
	t.Logf("Response content parts: %d", len(resp.Message.Content))
	for i, part := range resp.Message.Content {
		switch p := part.(type) {
		case core.TextPart:
			t.Logf("  [%d] text: %s", i, p.Text)
		case core.ToolCallPart:
			t.Logf("  [%d] tool_call: name=%s args=%s id=%s", i, p.Name, p.Arguments, p.ID)
		}
	}
	t.Logf("Finish reason: %s", resp.FinishReason)
}

func TestIntegration_ObjectGeneration(t *testing.T) {
	cfg := integrationTestConfig(t)
	c := newIntegrationClient(t, cfg)

	resp, err := c.ChatCompletion(context.Background(), cfg.Model, &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate a JSON object with a greeting field."}}},
		},
		ResponseFormat: &core.ResponseFormat{Type: core.ResponseFormatTypeJSON},
	})
	if err != nil {
		t.Fatalf("ChatCompletion (JSON mode) error: %v", err)
	}
	if len(resp.Message.Content) == 0 {
		t.Fatal("expected at least one content part in response")
	}

	objResp, err := ExtractObjectResponse(resp, cfg.Model)
	if err != nil {
		t.Fatalf("ExtractObjectResponse error: %v", err)
	}
	if objResp.Object == nil {
		t.Fatal("expected object in response")
	}
	t.Logf("Generated object: %+v", objResp.Object)
}

func TestIntegration_Rerank(t *testing.T) {
	baseURL := os.Getenv("OPENAICOMPAT_BASE_URL")
	apiKey := os.Getenv("OPENAICOMPAT_API_KEY")
	model := os.Getenv("OPENAICOMPAT_RERANK_MODEL")
	if baseURL == "" || apiKey == "" || model == "" {
		t.Skip("Skipping rerank integration test: set OPENAICOMPAT_BASE_URL, OPENAICOMPAT_API_KEY, OPENAICOMPAT_RERANK_MODEL to run")
	}

	c := NewClient(baseURL, apiKey)
	c.RerankFormat = RerankFormatOpenAICompatible

	resp, err := c.CreateRerank(context.Background(), model, &rerank.RerankRequest{
		Query: "What is the capital of France?",
		Documents: []string{
			"Paris is the capital and most populous city of France.",
			"Berlin is the capital of Germany.",
			"Madrid is the capital of Spain.",
			"The Eiffel Tower is located in Paris.",
		},
		TopN:            3,
		ReturnDocuments: true,
	})
	if err != nil {
		t.Fatalf("CreateRerank error: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one result")
	}
	if len(resp.Results) > 3 {
		t.Errorf("expected at most 3 results, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		t.Logf("Result[%d]: index=%d score=%.4f text=%q", i, r.Index, r.RelevanceScore, r.Document)
	}
	t.Logf("Usage: prompt=%d total=%d", resp.Usage.PromptTokens, resp.Usage.TotalTokens)
}
