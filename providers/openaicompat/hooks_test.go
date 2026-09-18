package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestHooks_MapFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "test-model",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "Done"},
				FinishReason: ptr("end_turn"),
			}},
			Usage: &Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.Hooks.MapFinishReason = func(fr string) string {
		if fr == "end_turn" {
			return "stop"
		}
		return fr
	}

	resp, err := c.ChatCompletion(context.Background(), "test-model", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish_reason mapped to 'stop', got %q", resp.FinishReason)
	}
}

func TestHooks_PostProcessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatCompletionResponse{
			Model: "test-model",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "Hello"},
				FinishReason: ptr("stop"),
			}},
			Usage: &Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.Hooks.PostProcessResponse = func(resp *core.Response, raw *ChatCompletionResponse) {
		resp.Usage.TotalTokens += 100 // artificial adjustment
	}

	resp, err := c.ChatCompletion(context.Background(), "test-model", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.TotalTokens != 106 {
		t.Errorf("expected total_tokens=106 after hook, got %d", resp.Usage.TotalTokens)
	}
}

func TestHooks_PostProcessStreamPart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []ChatCompletionResponse{
			{Model: "test-model", Choices: []Choice{{
				Delta: Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "test-model", Choices: []Choice{{
				Delta:        Message{Content: ""},
				FinishReason: ptr("stop"),
			}}},
		}
		for _, c := range chunks {
			data, _ := json.Marshal(c)
			w.Write([]byte("data: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.Hooks.PostProcessStreamPart = func(part *core.StreamPart, raw *ChatCompletionResponse) {
		if part.Type == core.StreamPartTypeTextDelta {
			part.TextDelta = "[hook]" + part.TextDelta
		}
	}

	stream := c.ChatCompletionStream(context.Background(), "test-model", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	var textDeltas []string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeTextDelta {
			textDeltas = append(textDeltas, part.TextDelta)
		}
	}

	if len(textDeltas) != 1 || textDeltas[0] != "[hook]Hello" {
		t.Errorf("unexpected text deltas: %v", textDeltas)
	}
}

func TestHooks_PrepareRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if req.User != "alice" {
			t.Errorf("expected user=alice, got %q", req.User)
		}
		resp := ChatCompletionResponse{
			Model: "test-model",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "OK"},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.Hooks.PrepareRequest = func(req *ChatCompletionRequest, model string, coreReq *core.Request) {
		req.User = "alice"
	}

	_, err := c.ChatCompletion(context.Background(), "test-model", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
