package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)


func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role:    "assistant",
					Content: "Hello!",
				},
				FinishReason: ptr("stop"),
			}},
			Usage: &Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello!" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestGenerateWithTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "get_weather", Arguments: `{"city":"NYC"}`},
					}},
				},
				FinishReason: ptr("tool_calls"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Weather?"}}}},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if tc, ok := resp.Message.Content[0].(core.ToolCallPart); !ok || tc.Name != "get_weather" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
}

func TestGenerateObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role:    "assistant",
					Content: `{"name":"test","value":42}`,
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate an object"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeJSON,
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "test" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
	if resp.Object["value"] != float64(42) {
		t.Errorf("unexpected value: %+v", resp.Object)
	}
}

func TestGenerateObject_ToolMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role: "assistant",
					ToolCalls: []ToolCall{{
						ID:   "call_obj",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "generate_object", Arguments: `{"name":"tool","value":99}`},
					}},
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeTool,
	})
	if err != nil {
		t.Fatalf("generate object (tool mode): %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "tool" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
}

func TestGenerateObject_TextMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role:    "assistant",
					Content: `{"name":"text","value":1}`,
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeText,
	})
	if err != nil {
		t.Fatalf("generate object (text mode): %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "text" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
}

func TestGenerateObject_AutoMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role:    "assistant",
					Content: `{"name":"auto","value":0}`,
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeAuto,
	})
	if err != nil {
		t.Fatalf("generate object (auto mode): %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "auto" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
}

func TestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "moonshot-v1-8k", Choices: []Choice{{
				Delta: Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "moonshot-v1-8k", Choices: []Choice{{
				Delta: Message{Content: " world"},
			}}},
			{Model: "moonshot-v1-8k", Choices: []Choice{{
				Delta:        Message{Content: ""},
				FinishReason: ptr("stop"),
			}}},
		}
		for _, c := range chunks {
			data, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	stream, err := model.Stream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("stream init: %v", err)
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

	got := ""
	for _, d := range textDeltas {
		got += d
	}
	if got != "Hello world" {
		t.Errorf("text deltas: got %q, want %q", got, "Hello world")
	}
	if finishReason != "stop" {
		t.Errorf("finish reason: got %q, want stop", finishReason)
	}
}

func TestGenerateWithReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "moonshot-v1-8k",
			Choices: []Choice{{
				Message: Message{
					Role:             "assistant",
					Content:          "The answer is 4.",
					ReasoningContent: "Let me think...",
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "What is 2+2?"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(resp.Message.Content))
	}
	rp, ok := resp.Message.Content[0].(core.ReasoningPart)
	if !ok || rp.Text != "Let me think..." {
		t.Errorf("expected reasoning part, got: %+v", resp.Message.Content[0])
	}
}

func TestProviderAndModel(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lm, _ := p.LanguageModel(context.Background(), "kimi-k2")
	if lm.Provider() != "kimi" {
		t.Errorf("expected provider kimi, got %s", lm.Provider())
	}
	if lm.Model() != "kimi-k2" {
		t.Errorf("expected model kimi-k2, got %s", lm.Model())
	}
}

func TestGenerate_RequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "bad request"}`))
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	_, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// Live tests against the real Kimi API.


func TestLive_Generate(t *testing.T) {
	key := liveAPIKey(t)
	p, err := New(key)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	model, err := p.LanguageModel(context.Background(), "kimi-k2-turbo-preview")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Say hello in one word"}}},
		},
		MaxTokens: intPtr(100),
	})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if len(resp.Message.Content) == 0 {
		t.Fatal("expected content in response")
	}
	var text string
	var reasoning string
	for _, part := range resp.Message.Content {
		switch p := part.(type) {
		case core.TextPart:
			text = p.Text
		case core.ReasoningPart:
			reasoning = p.Text
		}
	}
	if reasoning != "" {
		t.Logf("Live reasoning: %s", reasoning)
	}
	if text == "" && reasoning == "" {
		t.Error("expected non-empty text or reasoning response")
	}
	t.Logf("Live response: %s", text)
	t.Logf("Usage: prompt=%d completion=%d total=%d", resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
}

func TestLive_Stream(t *testing.T) {
	key := liveAPIKey(t)
	p, err := New(key)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	model, err := p.LanguageModel(context.Background(), "kimi-k2-turbo-preview")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	stream, err := model.Stream(context.Background(), &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Count from 1 to 3"}}},
		},
		MaxTokens: intPtr(100),
	})
	if err != nil {
		t.Fatalf("Stream init error: %v", err)
	}

	var textDeltas []string
	var reasoningDeltas []string
	var finishReason string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		case core.StreamPartTypeReasoningDelta:
			reasoningDeltas = append(reasoningDeltas, part.ReasoningDelta)
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		}
	}

	fullText := ""
	for _, d := range textDeltas {
		fullText += d
	}
	fullReasoning := ""
	for _, d := range reasoningDeltas {
		fullReasoning += d
	}
	if fullText == "" && fullReasoning == "" {
		t.Error("expected non-empty streamed text or reasoning")
	}
	if finishReason == "" {
		t.Error("expected non-empty finish reason")
	}
	t.Logf("Streamed text: %s", fullText)
	t.Logf("Streamed reasoning: %s", fullReasoning)
	t.Logf("Finish reason: %s", finishReason)
}

func TestLive_GenerateWithTool(t *testing.T) {
	key := liveAPIKey(t)
	p, err := New(key)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	model, err := p.LanguageModel(context.Background(), "kimi-k2-turbo-preview")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	resp, err := model.Generate(context.Background(), &core.Request{
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

func TestLive_GenerateObject(t *testing.T) {
	key := liveAPIKey(t)
	p, err := New(key)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	model, err := p.LanguageModel(context.Background(), "kimi-k2-turbo-preview")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
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

func intPtr(n int) *int { return &n }
