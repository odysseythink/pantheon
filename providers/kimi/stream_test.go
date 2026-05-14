package kimi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestChatCompletionStream_Basic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Say hello"}}},
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

	fullText := strings.Join(textDeltas, "")
	if fullText != "Hello world" {
		t.Errorf("expected 'Hello world', got '%s'", fullText)
	}
	if finishReason != "stop" {
		t.Errorf("expected finish reason 'stop', got '%s'", finishReason)
	}
}

func TestChatCompletionStream_ReasoningDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"Let me think...\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The answer is 4.\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "What is 2+2?"}}}},
	})

	var reasoningDeltas []string
	var textDeltas []string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeReasoningDelta:
			reasoningDeltas = append(reasoningDeltas, part.ReasoningDelta)
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		}
	}

	if strings.Join(reasoningDeltas, "") != "Let me think..." {
		t.Errorf("expected reasoning 'Let me think...', got '%s'", strings.Join(reasoningDeltas, ""))
	}
	if strings.Join(textDeltas, "") != "The answer is 4." {
		t.Errorf("expected text 'The answer is 4.', got '%s'", strings.Join(textDeltas, ""))
	}
}

func TestChatCompletionStream_ToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}}]},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
	})

	var toolCall *core.ToolCallPart
	var finishReason string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeToolCall:
			toolCall = part.ToolCall
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		}
	}

	if toolCall == nil {
		t.Fatal("expected a tool call")
	}
	if toolCall.Name != "get_weather" {
		t.Errorf("expected tool name get_weather, got %s", toolCall.Name)
	}
	if toolCall.Arguments != `{"city":"NYC"}` {
		t.Errorf("expected arguments '{\"city\":\"NYC\"}', got %s", toolCall.Arguments)
	}
	if finishReason != "tool_calls" {
		t.Errorf("expected finish reason tool_calls, got %s", finishReason)
	}
}

func TestChatCompletionStream_UsageOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	var usage *core.Usage
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeUsage {
			usage = part.Usage
		}
	}

	if usage == nil {
		t.Fatal("expected usage in stream")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 2 || usage.TotalTokens != 12 {
		t.Errorf("unexpected usage: %+v", usage)
	}
}

func TestChatCompletionStream_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": "rate limit"}`))
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	for _, err := range stream {
		if err == nil {
			t.Fatal("expected error")
		}
		pe, ok := err.(*core.ProviderError)
		if !ok {
			t.Fatalf("expected *core.ProviderError, got %T", err)
		}
		if pe.Status != 429 {
			t.Errorf("expected status 429, got %d", pe.Status)
		}
		return
	}
	t.Fatal("expected error from stream")
}

func TestChatCompletionStream_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: not-json")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})

	for _, err := range stream {
		if err == nil {
			t.Fatal("expected error")
		}
		return
	}
	t.Fatal("expected error from stream")
}
