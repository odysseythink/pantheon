package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odysseythink/ai/core"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := GenerateContentResponse{
			Candidates: []Candidate{{
				Content: Content{
					Role:  "model",
					Parts: []Part{{Text: "Hello from Gemini!"}},
				},
				FinishReason: "STOP",
				Index:        0,
			}},
			UsageMetadata: &UsageMetadata{
				PromptTokenCount:     5,
				CandidatesTokenCount: 4,
				TotalTokenCount:      9,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, err := New("test-key", WithBaseURL(server.URL))
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	model, err := p.LanguageModel(context.Background(), "gemini-pro")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello from Gemini!" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
	if resp.FinishReason != "STOP" {
		t.Errorf("finish reason: got %q, want STOP", resp.FinishReason)
	}
	if resp.Usage.PromptTokens != 5 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 9 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestGenerateWithTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{{
				Content: Content{
					Role: "model",
					Parts: []Part{{
						FunctionCall: &FunctionCall{
							Name: "get_weather",
							Args: map[string]interface{}{"city": "Paris"},
						},
					}},
				},
				FinishReason: "STOP",
				Index:        0,
			}},
			UsageMetadata: &UsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
				TotalTokenCount:      15,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gemini-pro")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "What's the weather?"}}}},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters: &core.Schema{
				Type: "object",
				Properties: map[string]*core.Schema{
					"city": {Type: "string"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) == 0 {
		t.Fatal("no content")
	}
	tcp, ok := resp.Message.Content[0].(core.ToolCallPart)
	if !ok {
		t.Fatalf("expected ToolCallPart, got %T", resp.Message.Content[0])
	}
	if tcp.Name != "get_weather" {
		t.Errorf("tool name: got %q, want get_weather", tcp.Name)
	}
	if tcp.Arguments != `{"city":"Paris"}` {
		t.Errorf("tool arguments: got %q", tcp.Arguments)
	}
}

func TestGenerateObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{{
				Content: Content{
					Role:  "model",
					Parts: []Part{{Text: `{"name":"Alice","age":30}`}},
				},
				FinishReason: "STOP",
				Index:        0,
			}},
			UsageMetadata: &UsageMetadata{
				PromptTokenCount:     8,
				CandidatesTokenCount: 6,
				TotalTokenCount:      14,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gemini-pro")

	resp, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Give me a person"}}}},
		Schema: &core.Schema{
			Type: "object",
			Properties: map[string]*core.Schema{
				"name": {Type: "string"},
				"age":  {Type: "integer"},
			},
		},
		Mode: core.ObjectModeJSON,
	})
	if err != nil {
		t.Fatalf("generate object: %v", err)
	}
	if resp.Object["name"] != "Alice" {
		t.Errorf("name: got %v, want Alice", resp.Object["name"])
	}
	if resp.Object["age"] != float64(30) {
		t.Errorf("age: got %v, want 30", resp.Object["age"])
	}
}

func TestStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"index":0}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":" Gemini"}]},"index":0}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP","index":0}]}`,
			`{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":3,"totalTokenCount":6}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintln(w, chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gemini-pro")

	stream, err := model.Stream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}

	var textDeltas []string
	var finishReason string
	var usage *core.Usage
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		case core.StreamPartTypeUsage:
			usage = part.Usage
		}
	}

	got := ""
	for _, d := range textDeltas {
		got += d
	}
	if got != "Hello Gemini!" {
		t.Errorf("text deltas: got %q, want %q", got, "Hello Gemini!")
	}
	if finishReason != "STOP" {
		t.Errorf("finish reason: got %q, want STOP", finishReason)
	}
	if usage == nil || usage.PromptTokens != 3 || usage.CompletionTokens != 3 || usage.TotalTokens != 6 {
		t.Errorf("usage: %+v", usage)
	}
}

func TestGenerateBlockedPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GenerateContentResponse{
			Candidates: []Candidate{},
			PromptFeedback: &PromptFeedback{
				BlockReason: "SAFETY",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gemini-pro")

	_, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Bad prompt"}}}},
	})
	if err == nil {
		t.Fatal("expected error for blocked prompt")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Code != "SAFETY" {
		t.Errorf("error code: got %q, want SAFETY", pe.Code)
	}
}

func TestGenerateValidatesAPIKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}
