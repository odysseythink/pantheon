package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/types"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := openaicompat.ChatCompletionResponse{
			Model: "deepseek-chat",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role:    "assistant",
					Content: "Hello!",
				},
				FinishReason: ptr("stop"),
			}},
			Usage: &openaicompat.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
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
		resp := openaicompat.ChatCompletionResponse{
			Model: "deepseek-chat",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
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
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
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
		resp := openaicompat.ChatCompletionResponse{
			Model: "deepseek-chat",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
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
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate an object"}}}},
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

func TestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []openaicompat.ChatCompletionResponse{
			{Model: "deepseek-chat", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "deepseek-chat", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Content: " world"},
			}}},
			{Model: "deepseek-chat", Choices: []openaicompat.Choice{{
				Delta:        openaicompat.Message{Content: ""},
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
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	stream, err := model.Stream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
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

func TestGenerateObject_ToolMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ChatCompletionResponse{
			Model: "deepseek-chat",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "generate_object", Arguments: `{"name":"tool","value":99}`},
					}},
				},
				FinishReason: ptr("tool_calls"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate an object"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeTool,
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "tool" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
	if resp.Object["value"] != float64(99) {
		t.Errorf("unexpected value: %+v", resp.Object)
	}
}

func TestGenerateObject_TextMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ChatCompletionResponse{
			Model: "deepseek-chat",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role:    "assistant",
					Content: `{"name":"text","value":42}`,
				},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate an object"}}}},
		Schema: &core.Schema{Type: "object", Properties: map[string]*core.Schema{
			"name":  {Type: "string"},
			"value": {Type: "integer"},
		}},
		Mode: core.ObjectModeText,
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["name"] != "text" {
		t.Errorf("unexpected name: %+v", resp.Object)
	}
	if resp.Object["value"] != float64(42) {
		t.Errorf("unexpected value: %+v", resp.Object)
	}
}

func TestGenerateObject_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid key"})
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "deepseek-chat")

	_, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate an object"}}}},
		Schema:   &core.Schema{Type: "object"},
		Mode:     core.ObjectModeAuto,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func ptr(s string) *string { return &s }
