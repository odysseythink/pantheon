package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/types"
)

func TestChatCompletionStream_Text(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if accept := r.Header.Get("Accept"); accept != "text/event-stream" {
			t.Errorf("expected Accept text/event-stream, got %q", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{Content: " world"},
			}}},
			{Model: "gpt-4", Choices: []Choice{{
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

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
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

func TestChatCompletionStream_WithUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{Role: "assistant", Content: "Hi"},
			}}},
			{Model: "gpt-4", Choices: []Choice{}, Usage: &Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6}},
			{Model: "gpt-4", Choices: []Choice{{
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

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	var usageFound bool
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeUsage && part.Usage != nil {
			usageFound = true
			if part.Usage.TotalTokens != 6 {
				t.Errorf("usage: %+v", part.Usage)
			}
		}
	}
	if !usageFound {
		t.Error("expected usage event")
	}
}

func TestChatCompletionStream_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	for _, err := range stream {
		if err != nil {
			pe, ok := err.(*core.ProviderError)
			if !ok {
				t.Fatalf("expected ProviderError, got %T", err)
			}
			if pe.Status != 429 {
				t.Errorf("expected status 429, got %d", pe.Status)
			}
			return
		}
	}
	t.Fatal("expected error in stream")
}

func TestChatCompletionStream_ToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{
					Role: "assistant",
					ToolCalls: []types.ToolCall{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "get_weather", Arguments: `{"city":"NYC"}`},
					}},
				},
				FinishReason: ptr("tool_calls"),
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

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
	})

	var toolCallFound bool
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeToolCall && part.ToolCall != nil {
			toolCallFound = true
			if part.ToolCall.Name != "get_weather" {
				t.Errorf("unexpected tool call: %+v", part.ToolCall)
			}
		}
	}
	if !toolCallFound {
		t.Error("expected tool call event")
	}
}

func TestChatCompletionStream_ToolInputDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{
					ToolCalls: []types.ToolCall{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "search", Arguments: `{"q":`},
					}},
				},
			}}},
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{
					ToolCalls: []types.ToolCall{{
						Index: 0,
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Arguments: `"hello"`},
					}},
				},
			}}},
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{
					ToolCalls: []types.ToolCall{{
						Index: 0,
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Arguments: `}`},
					}},
				},
			}}},
			{Model: "gpt-4", Choices: []Choice{{
				FinishReason: ptr("tool_calls"),
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

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Search?"}}}},
	})

	var types []core.StreamPartType
	var deltas []string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		types = append(types, part.Type)
		if part.Type == core.StreamPartTypeToolInputDelta && part.ToolCall != nil {
			deltas = append(deltas, part.ToolCall.Arguments)
		}
	}

	wantTypes := []core.StreamPartType{
		core.StreamPartTypeToolInputStart,
		core.StreamPartTypeToolInputDelta,
		core.StreamPartTypeToolInputDelta,
		core.StreamPartTypeToolInputDelta,
		core.StreamPartTypeToolInputEnd,
		core.StreamPartTypeToolCall,
		core.StreamPartTypeFinish,
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("part types count mismatch: got %d, want %d\ngot: %v", len(types), len(wantTypes), types)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Fatalf("part type[%d]: got %v, want %v", i, types[i], wantTypes[i])
		}
	}

	wantDeltas := []string{`{"q":`, `"hello"`, `}`}
	if len(deltas) != len(wantDeltas) {
		t.Fatalf("deltas count mismatch: got %d, want %d", len(deltas), len(wantDeltas))
	}
	for i := range wantDeltas {
		if deltas[i] != wantDeltas[i] {
			t.Fatalf("delta[%d]: got %q, want %q", i, deltas[i], wantDeltas[i])
		}
	}
}
