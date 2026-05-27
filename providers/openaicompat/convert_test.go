package openaicompat

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/types"
)

func TestToOpenAIMessages_SystemPrompt(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
	}
	out, err := ToOpenAIMessages(msgs, "You are a helper")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "system" || out[0].Content != "You are a helper" {
		t.Errorf("unexpected system message: %+v", out[0])
	}
	if out[1].Role != "user" {
		t.Errorf("unexpected user message role: %s", out[1].Role)
	}
}

func TestToOpenAIMessages_UserText(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}},
	}
	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "user" || out[0].Content != "Hi" {
		t.Errorf("unexpected message: %+v", out[0])
	}
}

func TestToOpenAIMessages_UserMultimodal(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{
			core.TextPart{Text: "What is this?"},
			core.ImagePart{URL: "http://example.com/img.png", Detail: "high"},
		}},
	}
	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parts, ok := out[0].Content.([]ContentParter)
	if !ok {
		t.Fatalf("expected []ContentParter, got %T", out[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "What is this?" {
		t.Errorf("unexpected first part: %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "http://example.com/img.png" {
		t.Errorf("unexpected second part: %+v", parts[1])
	}
}

func TestToOpenAIMessages_AssistantText(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hello!"}}},
	}
	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "assistant" || out[0].Content != "Hello!" {
		t.Errorf("unexpected message: %+v", out[0])
	}
}

func TestToOpenAIMessages_AssistantWithToolCalls(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.TextPart{Text: "Let me check"},
			core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		}},
	}
	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "assistant" {
		t.Errorf("unexpected role: %s", out[0].Role)
	}
	if out[0].Content != "Let me check" {
		t.Errorf("unexpected content: %v", out[0].Content)
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out[0].ToolCalls))
	}
	if out[0].ToolCalls[0].ID != "call_1" || out[0].ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", out[0].ToolCalls[0])
	}
}

func TestToOpenAIMessages_ToolResult(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_TOOL, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "call_1", Name: "get_weather", Content: []core.ContentParter{core.TextPart{Text: "Sunny"}}},
		}},
	}
	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "tool" || out[0].ToolCallID != "call_1" || out[0].Content != "Sunny" {
		t.Errorf("unexpected message: %+v", out[0])
	}
}

func TestToOpenAIMessages_UnsupportedAssistantPart(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ImagePart{URL: "http://example.com/img.png"},
		}},
	}
	_, err := ToOpenAIMessages(msgs, "")
	if err == nil {
		t.Fatal("expected error for unsupported content part")
	}
}

func TestToOpenAIMessages_UnsupportedUserPart(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{
			core.AudioPart{URL: "http://example.com/audio.mp3"},
		}},
	}
	_, err := ToOpenAIMessages(msgs, "")
	if err == nil {
		t.Fatal("expected error for unsupported content part")
	}
}

func TestContentToString(t *testing.T) {
	parts := []core.ContentParter{
		core.TextPart{Text: "Hello"},
		core.TextPart{Text: "World"},
	}
	got := contentToString(parts)
	if got != "Hello\nWorld" {
		t.Errorf("got %q, want %q", got, "Hello\nWorld")
	}
}

func TestJoinTexts(t *testing.T) {
	got := joinTexts([]string{"a", "b", "c"})
	if got != "a\nb\nc" {
		t.Errorf("got %q, want %q", got, "a\nb\nc")
	}
	if joinTexts(nil) != "" {
		t.Errorf("expected empty string for nil input")
	}
}

func TestToolResultCallID(t *testing.T) {
	parts := []core.ContentParter{
		core.TextPart{Text: "foo"},
		core.ToolResultPart{ToolCallID: "call_123"},
	}
	if got := toolResultCallID(parts); got != "call_123" {
		t.Errorf("got %q, want call_123", got)
	}
	if got := toolResultCallID(nil); got != "" {
		t.Errorf("expected empty string for nil input")
	}
}

func TestToOpenAITools(t *testing.T) {
	tools := []core.ToolDefinition{
		{Name: "get_weather", Description: "Get weather", Parameters: &core.Schema{Type: "object"}},
	}
	out := ToOpenAITools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	tool, ok := out[0].(Tool)
	if !ok {
		t.Fatalf("expected Tool, got %T", out[0])
	}
	if tool.Type != "function" || tool.Function.Name != "get_weather" {
		t.Errorf("unexpected tool: %+v", tool)
	}
	if len(ToOpenAITools(nil)) != 0 {
		t.Errorf("expected empty slice for nil input")
	}
}

func TestToOpenAITools_ProviderTool(t *testing.T) {
	tools := []core.ToolDefinition{
		{Name: "web_search", ProviderTool: map[string]string{"type": "web_search"}},
		{Name: "get_weather", Description: "Get weather", Parameters: &core.Schema{Type: "object"}},
	}
	out := ToOpenAITools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}

	// First tool should be the provider-native one
	m, ok := out[0].(map[string]string)
	if !ok {
		t.Fatalf("expected map for provider tool, got %T", out[0])
	}
	if m["type"] != "web_search" {
		t.Errorf("unexpected provider tool: %+v", m)
	}

	// Second tool should be a function tool
	tool, ok := out[1].(Tool)
	if !ok {
		t.Fatalf("expected Tool, got %T", out[1])
	}
	if tool.Type != "function" {
		t.Errorf("expected function type, got %q", tool.Type)
	}
}

func TestToOpenAITools_ProviderDefinedTool(t *testing.T) {
	tools := []core.ToolDefinition{
		{
			Name: "web_search",
			ProviderTool: &core.ProviderDefinedTool{
				ID:   "openai.web_search_preview",
				Name: "web_search",
			},
		},
		{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		},
	}
	out := ToOpenAITools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}
	m, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map for provider tool, got %T", out[0])
	}
	if m["type"] != "web_search_preview" {
		t.Errorf("unexpected provider tool: %+v", m)
	}
	tool, ok := out[1].(Tool)
	if !ok {
		t.Fatalf("expected Tool, got %T", out[1])
	}
	if tool.Type != "function" {
		t.Errorf("expected function type, got %q", tool.Type)
	}
}

func TestToOpenAITools_ProviderDefinedToolUnknownID(t *testing.T) {
	opaque := map[string]string{"type": "custom"}
	tools := []core.ToolDefinition{
		{
			Name:         "custom_tool",
			ProviderTool: &core.ProviderDefinedTool{ID: "unknown.custom", Name: "custom_tool"},
		},
		{
			Name:         "opaque_tool",
			ProviderTool: opaque,
		},
	}
	out := ToOpenAITools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}
	// Unknown ProviderDefinedTool falls back to opaque passthrough
	pdt, ok := out[0].(*core.ProviderDefinedTool)
	if !ok {
		t.Fatalf("expected *ProviderDefinedTool fallback, got %T", out[0])
	}
	if pdt.ID != "unknown.custom" {
		t.Errorf("unexpected fallback: %+v", pdt)
	}
	// Non-ProviderDefinedTool opaque passthrough
	m, ok := out[1].(map[string]string)
	if !ok {
		t.Fatalf("expected map for opaque tool, got %T", out[1])
	}
	if m["type"] != "custom" {
		t.Errorf("unexpected opaque tool: %+v", m)
	}
}

func TestToCoreResponse_Text(t *testing.T) {
	resp := &ChatCompletionResponse{
		Model: "gpt-4",
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hello!"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}
	coreResp, err := ToCoreResponse(resp, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(coreResp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(coreResp.Message.Content))
	}
	if tp, ok := coreResp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello!" {
		t.Errorf("unexpected content: %+v", coreResp.Message.Content[0])
	}
	if coreResp.FinishReason != "stop" {
		t.Errorf("unexpected finish reason: %s", coreResp.FinishReason)
	}
	if coreResp.Usage.TotalTokens != 12 {
		t.Errorf("unexpected usage: %+v", coreResp.Usage)
	}
	if coreResp.Model != "gpt-4" {
		t.Errorf("unexpected model: %s", coreResp.Model)
	}
}

func TestToCoreResponse_ToolCall(t *testing.T) {
	resp := &ChatCompletionResponse{
		Model: "gpt-4",
		Choices: []Choice{{
			Message: Message{
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
	coreResp, err := ToCoreResponse(resp, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(coreResp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(coreResp.Message.Content))
	}
	if tc, ok := coreResp.Message.Content[0].(core.ToolCallPart); !ok || tc.Name != "get_weather" {
		t.Errorf("unexpected content: %+v", coreResp.Message.Content[0])
	}
}

func TestToCoreResponse_EmptyChoices(t *testing.T) {
	resp := &ChatCompletionResponse{Model: "gpt-4", Choices: []Choice{}}
	_, err := ToCoreResponse(resp, "gpt-4")
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestToCoreResponse_NoUsage(t *testing.T) {
	resp := &ChatCompletionResponse{
		Model: "gpt-4",
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hi"},
			FinishReason: ptr("stop"),
		}},
	}
	coreResp, err := ToCoreResponse(resp, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if coreResp.Usage.TotalTokens != 0 {
		t.Errorf("expected zero usage, got %+v", coreResp.Usage)
	}
}

func ptr(s string) *string { return &s }
