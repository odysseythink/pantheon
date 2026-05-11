package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/odysseythink/ai/core"
	"github.com/odysseythink/ai/providers/anthropic"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/model/anthropic.claude-3-opus/invoke" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req anthropic.MessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "anthropic.claude-3-opus" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		if req.MaxTokens != 4096 {
			t.Errorf("unexpected max_tokens: %d", req.MaxTokens)
		}
		if req.System != "You are helpful" {
			t.Errorf("unexpected system: %v", req.System)
		}

		resp := anthropic.MessagesResponse{
			ID:   "msg_1",
			Type: "message",
			Role: "assistant",
			Content: []anthropic.Content{
				{Type: "text", Text: "Hello from Bedrock!"},
			},
			Model:      "anthropic.claude-3-opus",
			StopReason: ptr("end_turn"),
			Usage:      &anthropic.Usage{InputTokens: 5, OutputTokens: 4},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, err := New("test-ak", "test-sk", "us-east-1")
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	lm, err := p.LanguageModel(context.Background(), "anthropic.claude-3-opus")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}
	lm.(*LanguageModel).client.endpoint = server.URL

	resp, err := lm.Generate(context.Background(), &core.Request{
		Messages:     []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
		SystemPrompt: "You are helpful",
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello from Bedrock!" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
	if resp.FinishReason != "end_turn" {
		t.Errorf("unexpected finish reason: %s", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 4 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
}

func TestGenerateWithTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropic.MessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(req.Tools))
		}
		if req.ToolChoice == nil || req.ToolChoice.Type != "tool" {
			t.Errorf("unexpected tool_choice: %+v", req.ToolChoice)
		}

		resp := anthropic.MessagesResponse{
			ID:   "msg_2",
			Type: "message",
			Role: "assistant",
			Content: []anthropic.Content{
				{Type: "tool_use", ID: "tu_1", Name: "get_weather", Input: map[string]any{"city": "Paris"}},
			},
			Model:      "anthropic.claude-3-sonnet",
			StopReason: ptr("tool_use"),
			Usage:      &anthropic.Usage{InputTokens: 10, OutputTokens: 6},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-ak", "test-sk", "us-west-2")
	lm, _ := p.LanguageModel(context.Background(), "anthropic.claude-3-sonnet")
	lm.(*LanguageModel).client.endpoint = server.URL

	resp, err := lm.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "What's the weather?"}}}},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		}},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "get_weather"},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	tcp, ok := resp.Message.Content[0].(core.ToolCallPart)
	if !ok {
		t.Fatalf("expected ToolCallPart, got %T", resp.Message.Content[0])
	}
	if tcp.Name != "get_weather" {
		t.Errorf("unexpected tool name: %s", tcp.Name)
	}
	if tcp.Arguments != `{"city":"Paris"}` {
		t.Errorf("unexpected arguments: %s", tcp.Arguments)
	}
}

func TestGenerateValidatesRegion(t *testing.T) {
	_, err := New("ak", "sk", "")
	if err == nil {
		t.Fatal("expected error for empty region")
	}
}

func TestGenerateObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropic.MessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Tools) != 1 || req.Tools[0].Name != "generate_object" {
			t.Errorf("expected generate_object tool, got %+v", req.Tools)
		}

		resp := anthropic.MessagesResponse{
			ID:   "msg_3",
			Type: "message",
			Role: "assistant",
			Content: []anthropic.Content{
				{Type: "tool_use", ID: "tu_2", Name: "generate_object", Input: map[string]any{"name": "Alice", "age": 30}},
			},
			Model:      "anthropic.claude-3-haiku",
			StopReason: ptr("end_turn"),
			Usage:      &anthropic.Usage{InputTokens: 8, OutputTokens: 5},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-ak", "test-sk", "eu-west-1")
	lm, _ := p.LanguageModel(context.Background(), "anthropic.claude-3-haiku")
	lm.(*LanguageModel).client.endpoint = server.URL

	resp, err := lm.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate a person"}}}},
		Schema:   &core.Schema{Type: "object"},
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object["name"] != "Alice" {
		t.Errorf("unexpected name: %v", resp.Object["name"])
	}
	if resp.Object["age"] != 30.0 {
		t.Errorf("unexpected age: %v", resp.Object["age"])
	}
	if resp.Model != "anthropic.claude-3-haiku" {
		t.Errorf("unexpected model: %s", resp.Model)
	}
}

func encodeEventStreamMessage(payload []byte) []byte {
	var buf bytes.Buffer
	encoder := eventstream.NewEncoder()
	msg := eventstream.Message{Payload: payload}
	if err := encoder.Encode(&buf, msg); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/model/anthropic.claude-3-opus/invoke-with-response-stream" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req anthropic.MessagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "anthropic.claude-3-opus" {
			t.Errorf("unexpected model: %s", req.Model)
		}

		chunks := []anthropic.MessagesResponse{
			{
				Type:    "message",
				Role:    "assistant",
				Content: []anthropic.Content{{Type: "text", Text: "Hello"}},
				Model:   "anthropic.claude-3-opus",
			},
			{
				Type:    "message",
				Role:    "assistant",
				Content: []anthropic.Content{{Type: "text", Text: " Bedrock"}},
				Model:   "anthropic.claude-3-opus",
			},
			{
				Type:       "message",
				Role:       "assistant",
				Content:    []anthropic.Content{},
				Model:      "anthropic.claude-3-opus",
				StopReason: ptr("end_turn"),
			},
		}

		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			w.Write(encodeEventStreamMessage(data))
		}
	}))
	defer server.Close()

	p, err := New("test-ak", "test-sk", "us-east-1")
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	lm, err := p.LanguageModel(context.Background(), "anthropic.claude-3-opus")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}
	lm.(*LanguageModel).client.endpoint = server.URL

	stream, err := lm.Stream(context.Background(), &core.Request{
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
	if got != "Hello Bedrock" {
		t.Errorf("text deltas: got %q, want %q", got, "Hello Bedrock")
	}
	if finishReason != "end_turn" {
		t.Errorf("finish reason: got %q, want end_turn", finishReason)
	}
}

func ptr(s string) *string { return &s }
