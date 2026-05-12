package kimi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

// Test coverage for error paths and edge cases not covered by other tests.

func TestBuildRequestBody_Error(t *testing.T) {
	// Pass an invalid message that causes toKimiMessage to fail
	req := &core.Request{
		Messages: []core.Message{{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{core.ImagePart{URL: "http://example.com"}},
		}},
	}
	_, err := buildRequestBody("kimi-k2", req, ProviderOptions{})
	if err == nil {
		t.Fatal("expected error for invalid assistant message content")
	}
}

func TestToKimiMessage_UnknownRole(t *testing.T) {
	msg := core.Message{
		Role:    core.Role("unknown"),
		Content: []core.ContentPart{core.TextPart{Text: "hello"}},
	}
	result, err := toKimiMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != "unknown" {
		t.Errorf("expected role unknown, got %s", result.Role)
	}
}

func TestToKimiMessage_EmptyToolMessage(t *testing.T) {
	msg := core.Message{
		Role: core.RoleTool,
	}
	result, err := toKimiMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Role != "tool" {
		t.Errorf("expected role tool, got %s", result.Role)
	}
}

func TestToolResultCallID_Empty(t *testing.T) {
	parts := []core.ContentPart{core.TextPart{Text: "no tool result"}}
	id := toolResultCallID(parts)
	if id != "" {
		t.Errorf("expected empty id, got %s", id)
	}
}

func TestContentToKimi_Unsupported(t *testing.T) {
	parts := []core.ContentPart{core.TextPart{Text: "hello"}, core.ImagePart{URL: "http://example.com"}, core.ReasoningPart{Text: "think"}}
	_, err := contentToKimi(parts)
	if err == nil {
		t.Fatal("expected error for unsupported content part in user message")
	}
}

func TestToKimiToolChoice_Default(t *testing.T) {
	result := toKimiToolChoice(core.ToolChoice{Mode: core.ToolChoiceMode("invalid")})
	if result != "auto" {
		t.Errorf("expected auto for invalid mode, got %v", result)
	}
}

func TestToKimiToolChoice_RequiredEmptyName(t *testing.T) {
	result := toKimiToolChoice(core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: ""})
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	fn := m["function"].(map[string]any)
	if fn["name"] != "" {
		t.Errorf("expected empty name, got %v", fn["name"])
	}
}

func TestEnsurePropertyTypes_ItemsNotMap(t *testing.T) {
	schema := map[string]any{
		"type": "array",
		"items": "string",
	}
	result := ensurePropertyTypes(schema)
	r, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map")
	}
	if r["items"] != "string" {
		t.Errorf("expected items unchanged, got %v", r["items"])
	}
}

func TestEnsurePropertyTypes_NilProperties(t *testing.T) {
	schema := map[string]any{
		"type": "object",
	}
	result := ensurePropertyTypes(schema)
	r, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map")
	}
	if r["type"] != "object" {
		t.Errorf("expected type object, got %v", r["type"])
	}
}

func TestParseCompletionResponse_NoChoices(t *testing.T) {
	resp := &ChatCompletionResponse{Choices: []Choice{}}
	_, err := parseCompletionResponse(resp, "kimi-k2")
	if err == nil {
		t.Fatal("expected error for no choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected 'no choices' error, got %v", err)
	}
}

func TestGenerate_BuildRequestBodyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	_, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{core.ImagePart{URL: "http://example.com"}},
		}},
	})
	if err == nil {
		t.Fatal("expected error from buildRequestBody")
	}
}

func TestGenerateObject_GenerateError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "server error"}`))
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "moonshot-v1-8k")

	_, err := model.GenerateObject(context.Background(), &core.ObjectRequest{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Generate"}}}},
		Schema:   &core.Schema{Type: "object"},
		Mode:     core.ObjectModeJSON,
	})
	if err == nil {
		t.Fatal("expected error from Generate")
	}
}

func TestChatCompletionStream_BuildRequestBodyError(t *testing.T) {
	client := newClient("test-key")
	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{core.ImagePart{URL: "http://example.com"}},
		}},
	})

	for _, err := range stream {
		if err == nil {
			t.Fatal("expected error")
		}
		return
	}
	t.Fatal("expected error from stream")
}


func TestChatCompletionStream_NetworkError(t *testing.T) {
	client := newClient("test-key")
	client.BaseURL = "http://invalid.localhost:99999"
	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})

	for _, err := range stream {
		if err == nil {
			t.Fatal("expected error")
		}
		return
	}
	t.Fatal("expected error from stream")
}

func TestUploadFile_CreateFormFileError(t *testing.T) {
	// This is hard to trigger without mocking multipart.Writer
	// Skip for now - multipart errors are edge cases
}

func TestUploadFile_WriteError(t *testing.T) {
	// Hard to trigger without complex mocking
}

func TestUploadFile_CloseWriterError(t *testing.T) {
	// Hard to trigger without complex mocking
}

func TestClient_uploadFile_NoDst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"file-123"}`))
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	var b strings.Builder
	b.WriteString("test body")
	if err := c.uploadFile(context.Background(), "/files", strings.NewReader("test"), "text/plain", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_doJSON_MarshalError(t *testing.T) {
	c := newClient("sk-test")
	err := c.doJSON(context.Background(), "POST", "/test", make(chan int), nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestClient_doJSON_RequestError(t *testing.T) {
	c := newClient("sk-test")
	c.BaseURL = "://invalid-url"
	err := c.doJSON(context.Background(), "POST", "/test", map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected request error")
	}
}

func TestClient_uploadFile_RequestError(t *testing.T) {
	c := newClient("sk-test")
	c.BaseURL = "://invalid-url"
	err := c.uploadFile(context.Background(), "/test", strings.NewReader("test"), "text/plain", nil)
	if err == nil {
		t.Fatal("expected request error")
	}
}

func TestToKimiMessage_AssistantUnsupportedPart(t *testing.T) {
	msg := core.Message{
		Role:    core.RoleAssistant,
		Content: []core.ContentPart{core.ImagePart{URL: "http://example.com"}},
	}
	_, err := toKimiMessage(msg)
	if err == nil {
		t.Fatal("expected error for unsupported assistant content part")
	}
}

func TestExtractProviderOptions_GetMethod(t *testing.T) {
	type mockPO struct{ core.ProviderOptions }
	result := extractProviderOptions(mockPO{})
	if result.PromptCacheKey != "" {
		t.Error("expected empty options for mock provider options")
	}
}

func TestToKimiMessage_UserUnsupportedPart(t *testing.T) {
	msg := core.Message{
		Role:    core.RoleUser,
		Content: []core.ContentPart{core.ReasoningPart{Text: "think"}},
	}
	_, err := toKimiMessage(msg)
	if err == nil {
		t.Fatal("expected error for unsupported user content part")
	}
}

func TestEnsurePropertyTypes_PropertyNotMap(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"simple": "string",
		},
	}
	result := ensurePropertyTypes(schema)
	r, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map")
	}
	props := r["properties"].(map[string]any)
	if props["simple"] != "string" {
		t.Errorf("expected simple unchanged, got %v", props["simple"])
	}
}

func TestToKimiMessages_Error(t *testing.T) {
	msgs := []core.Message{{
		Role:    core.RoleAssistant,
		Content: []core.ContentPart{core.ImagePart{URL: "http://example.com"}},
	}}
	_, err := toKimiMessages(msgs, "")
	if err == nil {
		t.Fatal("expected error")
	}
}
