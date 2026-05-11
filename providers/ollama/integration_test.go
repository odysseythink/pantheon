package ollama

import (
	"context"
	"os"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func integrationTestConfig(t *testing.T) (baseURL, apiKey, model string) {
	t.Helper()
	baseURL = os.Getenv("OLLAMA_BASE_URL")
	apiKey = os.Getenv("OLLAMA_API_KEY")
	model = os.Getenv("OLLAMA_MODEL")
	if model == "" {
		t.Skip("Skipping integration test: set OLLAMA_MODEL to run (OLLAMA_BASE_URL and OLLAMA_API_KEY are optional)")
	}
	return baseURL, apiKey, model
}

func newIntegrationModel(t *testing.T, baseURL, apiKey, modelID string) core.LanguageModel {
	t.Helper()
	p, err := New(apiKey)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if baseURL != "" {
		p, err = New(apiKey, WithBaseURL(baseURL))
		if err != nil {
			t.Fatalf("new provider with base URL: %v", err)
		}
	}
	lm, err := p.LanguageModel(context.Background(), modelID)
	if err != nil {
		t.Fatalf("language model: %v", err)
	}
	return lm
}

func TestIntegration_Generate(t *testing.T) {
	baseURL, apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, baseURL, apiKey, model)

	resp, err := lm.Generate(context.Background(), &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Say hello in one word."}}},
		},
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if len(resp.Message.Content) == 0 {
		t.Fatal("expected content in response")
	}
	tp, ok := resp.Message.Content[0].(core.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", resp.Message.Content[0])
	}
	if tp.Text == "" {
		t.Error("expected non-empty text response")
	}
	t.Logf("Response: %s", tp.Text)
	t.Logf("Usage: prompt=%d completion=%d total=%d", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
}

func TestIntegration_Stream(t *testing.T) {
	baseURL, apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, baseURL, apiKey, model)

	stream, err := lm.Stream(context.Background(), &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Count from 1 to 3."}}},
		},
	})
	if err != nil {
		t.Fatalf("Stream init error: %v", err)
	}

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

func TestIntegration_GenerateWithTool(t *testing.T) {
	baseURL, apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, baseURL, apiKey, model)

	resp, err := lm.Generate(context.Background(), &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "What is the weather in Paris?"}}},
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
		t.Fatalf("Generate with tools error: %v", err)
	}

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

func TestIntegration_GenerateObject(t *testing.T) {
	baseURL, apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, baseURL, apiKey, model)

	resp, err := lm.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate a JSON object with a greeting field."}}},
		},
		Schema: &core.Schema{
			Type: "object",
			Properties: map[string]*core.Schema{
				"greeting": {Type: "string"},
			},
		},
		Mode: core.ObjectModeAuto,
	})
	if err != nil {
		t.Fatalf("GenerateObject error: %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object in response")
	}
	t.Logf("Generated object: %+v", resp.Object)
}
