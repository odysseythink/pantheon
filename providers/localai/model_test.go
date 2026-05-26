package localai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := openaicompat.ChatCompletionResponse{
			Model: "test-model",
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
	model, _ := p.LanguageModel(context.Background(), "test-model")

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
			{Model: "test-model", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Role: "assistant", Content: "Hello"},
			}}},
			{Model: "test-model", Choices: []openaicompat.Choice{{
				Delta: openaicompat.Message{Content: " world"},
			}}},
			{Model: "test-model", Choices: []openaicompat.Choice{{
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
	model, _ := p.LanguageModel(context.Background(), "test-model")

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

func TestGenerateObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ChatCompletionResponse{
			Model: "test-model",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role:    "assistant",
					Content: `{"city":"NYC","temp":72}`,
				},
				FinishReason: ptr("stop"),
			}},
			Usage: &openaicompat.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "test-model")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Give me weather"}}}},
		Schema:   &core.Schema{Type: "object"},
		Mode:     core.ObjectModeJSON,
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object == nil {
		t.Fatal("expected object")
	}
	if resp.Object["city"] != "NYC" {
		t.Errorf("unexpected object: %+v", resp.Object)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestGenerate_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "test-model")
	_, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func ptr(s string) *string { return &s }
