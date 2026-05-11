package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "api-version=") {
			t.Errorf("missing api-version in query: %s", r.URL.RawQuery)
		}
		if r.Header.Get("api-key") != "test-key" {
			t.Errorf("missing api-key header, got %q", r.Header.Get("api-key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		resp := openaicompat.ChatCompletionResponse{
			Model: "gpt-4",
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

	p, err := New("test-key", "res", "dep", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	model, _ := p.LanguageModel(context.Background(), "gpt-4")

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

func TestGenerateValidatesParams(t *testing.T) {
	_, err := New("key", "", "dep")
	if err == nil {
		t.Error("expected error for empty resourceName")
	}
	_, err = New("key", "res", "")
	if err == nil {
		t.Error("expected error for empty deployment")
	}
}

func TestGenerateWithTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role: "assistant",
					ToolCalls: []openaicompat.ToolCall{{
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

	p, _ := New("test-key", "res", "dep", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gpt-4")

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

func TestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []openaicompat.ChatCompletionResponse{
			{Model: "gpt-4", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "gpt-4", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Content: " world"},
			}}},
			{Model: "gpt-4", Choices: []openaicompat.Choice{{
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

	p, _ := New("test-key", "res", "dep", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gpt-4")

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

func ptr(s string) *string { return &s }
