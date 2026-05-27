package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestChatCompletionStream_IncompleteStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		// Send a text delta but no finish_reason
		chunks := []ChatCompletionResponse{
			{Model: "gpt-4", Choices: []Choice{{
				Delta: Message{Role: "assistant", Content: "Hello"},
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

	var incompleteErr error
	for part, err := range stream {
		if err != nil {
			incompleteErr = err
			break
		}
		_ = part
	}

	if incompleteErr == nil {
		t.Fatal("expected incomplete stream error")
	}
	if incompleteErr != core.ErrIncompleteStream {
		t.Errorf("expected ErrIncompleteStream, got %v", incompleteErr)
	}
}

func TestChatCompletionStream_IncompleteStream_NoDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		}
		for _, c := range chunks {
			data, _ := json.Marshal(c)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		// Close without [DONE]
		flusher.Flush()
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	stream := c.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	var incompleteErr error
	for part, err := range stream {
		if err != nil {
			incompleteErr = err
			break
		}
		_ = part
	}

	if incompleteErr == nil {
		t.Fatal("expected incomplete stream error")
	}
	if incompleteErr != core.ErrIncompleteStream {
		t.Errorf("expected ErrIncompleteStream, got %v", incompleteErr)
	}
}

func TestChatCompletionStream_CompleteStream_NoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	var errFound error
	for part, err := range stream {
		if err != nil {
			errFound = err
			break
		}
		_ = part
	}

	if errFound != nil {
		t.Fatalf("expected no error for complete stream, got %v", errFound)
	}
}
