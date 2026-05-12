package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openai"
)

func integrationTestConfig(t *testing.T) (apiKey, model string) {
	t.Helper()
	apiKey = os.Getenv("OPENAI_API_KEY")
	model = os.Getenv("OPENAI_MODEL")
	if apiKey == "" || model == "" {
		t.Skip("Skipping integration test: set OPENAI_API_KEY and OPENAI_MODEL to run")
	}
	return apiKey, model
}

func newIntegrationModel(t *testing.T, apiKey, modelID string) core.LanguageModel {
	t.Helper()
	p, err := openai.New(apiKey)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	lm, err := p.LanguageModel(context.Background(), modelID)
	if err != nil {
		t.Fatalf("language model: %v", err)
	}
	return lm
}

func TestIntegration_RunNoTools(t *testing.T) {
	apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, apiKey, model)

	a := New(lm, WithMaxSteps(5))
	res, err := a.Run(context.Background(), &Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Say hello in one word."}}},
		},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("expected messages in result")
	}
	lastMsg := res.Messages[len(res.Messages)-1]
	if len(lastMsg.Content) == 0 {
		t.Fatal("expected content in last message")
	}
	tp, ok := lastMsg.Content[0].(core.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", lastMsg.Content[0])
	}
	if tp.Text == "" {
		t.Error("expected non-empty text response")
	}
	t.Logf("Response: %s", tp.Text)
	t.Logf("Usage: prompt=%d completion=%d total=%d", res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens)
}

func TestIntegration_RunWithTool(t *testing.T) {
	apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, apiKey, model)

	a := New(lm, WithMaxSteps(5))
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		// Simple mock weather tool
		return `{"temperature": 22, "condition": "sunny"}`, nil
	})

	res, err := a.Run(context.Background(), &Request{
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
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("expected messages in result")
	}
	lastMsg := res.Messages[len(res.Messages)-1]
	if len(lastMsg.Content) == 0 {
		t.Fatal("expected content in last message")
	}
	tp, ok := lastMsg.Content[0].(core.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", lastMsg.Content[0])
	}
	if tp.Text == "" {
		t.Error("expected non-empty text response")
	}
	// Verify the agent actually used the tool by checking for tool result messages
	var foundToolResult bool
	for _, msg := range res.Messages {
		for _, part := range msg.Content {
			if tr, ok := part.(core.ToolResultPart); ok {
				foundToolResult = true
				t.Logf("Tool result: name=%s isError=%v content=%s", tr.Name, tr.IsError, tr.Content)
			}
		}
	}
	if !foundToolResult {
		t.Log("Warning: no tool result found; model may have answered without calling the tool")
	}
	t.Logf("Response: %s", tp.Text)
	t.Logf("Usage: prompt=%d completion=%d total=%d", res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens)
}

func TestIntegration_RunStream(t *testing.T) {
	apiKey, model := integrationTestConfig(t)
	lm := newIntegrationModel(t, apiKey, model)

	a := New(lm, WithMaxSteps(5))

	var textDeltas []string
	var lastUsage *core.Usage
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Count from 1 to 3."}}},
		},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch event.Type {
		case StreamEventTypeTextDelta:
			textDeltas = append(textDeltas, event.TextDelta)
		case StreamEventTypeUsage:
			lastUsage = event.Usage
		case StreamEventTypeStepStart, StreamEventTypeStepFinish:
			// expected
		}
	}

	fullText := strings.Join(textDeltas, "")
	if fullText == "" {
		t.Error("expected non-empty streamed text")
	}
	t.Logf("Streamed text: %s", fullText)
	if lastUsage != nil {
		t.Logf("Usage: prompt=%d completion=%d total=%d", lastUsage.PromptTokens, lastUsage.CompletionTokens, lastUsage.TotalTokens)
	}
}
