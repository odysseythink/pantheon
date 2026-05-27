package anthropic

import (
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToAnthropicMessages_UserText(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
	}
	got, err := ToAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Role != "user" {
		t.Errorf("expected role user, got %s", got[0].Role)
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Type != "text" || got[0].Content[0].Text != "Hello" {
		t.Errorf("unexpected content: %+v", got[0].Content)
	}
}

func TestToAnthropicMessages_AssistantText(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hi there"}}},
	}
	got, err := ToAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Role != "assistant" {
		t.Errorf("expected role assistant, got %s", got[0].Role)
	}
}

func TestToAnthropicMessages_ToolCall(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Paris"}`},
		}},
	}
	got, err := ToAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Type != "tool_use" {
		t.Errorf("unexpected content: %+v", got[0].Content)
	}
	if got[0].Content[0].ID != "call_1" || got[0].Content[0].Name != "get_weather" {
		t.Errorf("unexpected tool use fields: %+v", got[0].Content[0])
	}
	input, ok := got[0].Content[0].Input["city"]
	if !ok || input != "Paris" {
		t.Errorf("unexpected input: %+v", got[0].Content[0].Input)
	}
}

func TestToAnthropicMessages_ToolResult(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{
			core.ToolResultPart{
				ToolCallID: "call_1",
				Content:    []core.ContentParter{core.TextPart{Text: "Sunny"}},
				IsError:    false,
			},
		}},
	}
	got, err := ToAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Content) != 1 || got[0].Content[0].Type != "tool_result" {
		t.Errorf("unexpected content: %+v", got[0].Content)
	}
	if got[0].Content[0].ToolUseID != "call_1" {
		t.Errorf("unexpected tool_use_id: %s", got[0].Content[0].ToolUseID)
	}
}

func TestToAnthropicMessages_SystemPromptSkipped(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_SYSTEM, Content: []core.ContentParter{core.TextPart{Text: "You are helpful"}}},
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
	}
	got, err := ToAnthropicMessages(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content[0].Text != "Hello" {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestToAnthropicMessages_UnsupportedPartError(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.AudioPart{URL: "http://example.com/audio.mp3"}}},
	}
	_, err := ToAnthropicMessages(msgs)
	if err == nil {
		t.Fatal("expected error for unsupported content part")
	}
}

func TestToAnthropicContent_Text(t *testing.T) {
	parts := []core.ContentParter{core.TextPart{Text: "Hello world"}}
	got, err := toAnthropicContent(parts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "text" || got[0].Text != "Hello world" {
		t.Errorf("unexpected content: %+v", got)
	}
}

func TestToAnthropicContent_ImageWithData(t *testing.T) {
	parts := []core.ContentParter{core.ImagePart{Data: []byte("imagedata"), MIMEType: "image/png"}}
	got, err := toAnthropicContent(parts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "image" {
		t.Errorf("unexpected content: %+v", got)
	}
	if got[0].Source == nil {
		t.Fatal("expected image source")
	}
	if got[0].Source.Type != "base64" || got[0].Source.MediaType != "image/png" {
		t.Errorf("unexpected source: %+v", got[0].Source)
	}
	expectedData := base64.StdEncoding.EncodeToString([]byte("imagedata"))
	if got[0].Source.Data != expectedData {
		t.Errorf("expected data %s, got %s", expectedData, got[0].Source.Data)
	}
}

func TestToAnthropicContent_ImageURLError(t *testing.T) {
	parts := []core.ContentParter{core.ImagePart{URL: "http://example.com/image.png"}}
	_, err := toAnthropicContent(parts)
	if err == nil {
		t.Fatal("expected error for image URL")
	}
}

func TestToAnthropicContent_ToolCall(t *testing.T) {
	parts := []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "calc", Arguments: `{"a":1}`}}
	got, err := toAnthropicContent(parts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "tool_use" {
		t.Errorf("unexpected content: %+v", got)
	}
	if got[0].ID != "call_1" || got[0].Name != "calc" {
		t.Errorf("unexpected fields: %+v", got[0])
	}
}

func TestToAnthropicContent_ToolResult(t *testing.T) {
	parts := []core.ContentParter{
		core.ToolResultPart{
			ToolCallID: "call_1",
			Content:    []core.ContentParter{core.TextPart{Text: "result"}},
			IsError:    true,
		},
	}
	got, err := toAnthropicContent(parts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != "tool_result" {
		t.Errorf("unexpected content: %+v", got)
	}
	if got[0].ToolUseID != "call_1" || !got[0].IsError {
		t.Errorf("unexpected fields: %+v", got[0])
	}
	inner, ok := got[0].Content.([]Content)
	if !ok || len(inner) != 1 || inner[0].Text != "result" {
		t.Errorf("unexpected inner content: %+v", got[0].Content)
	}
}

func TestToAnthropicContent_UnsupportedPartError(t *testing.T) {
	parts := []core.ContentParter{core.DocumentPart{Data: []byte("pdf"), MIMEType: "application/pdf"}}
	_, err := toAnthropicContent(parts)
	if err == nil {
		t.Fatal("expected error for unsupported content part")
	}
}

func TestContentToString_MultipleTextParts(t *testing.T) {
	parts := []core.ContentParter{
		core.TextPart{Text: "Hello"},
		core.TextPart{Text: "world"},
	}
	got := contentToString(parts)
	want := "Hello\nworld"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestContentToString_ToolResultErrorPart(t *testing.T) {
	parts := []core.ContentParter{
		core.ToolResultErrorPart{Error: "connection failed"},
	}
	got := contentToString(parts)
	want := "connection failed"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestToAnthropicTools_NormalConversion(t *testing.T) {
	tools := []core.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters: &core.Schema{
				Type: "object",
			},
		},
	}
	got := ToAnthropicTools(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	tool, ok := got[0].(Tool)
	if !ok {
		t.Fatalf("expected Tool, got %T", got[0])
	}
	if tool.Name != "get_weather" || tool.Description != "Get weather" {
		t.Errorf("unexpected tool: %+v", tool)
	}
	schema, ok := tool.InputSchema.(*core.Schema)
	if !ok || schema.Type != "object" {
		t.Errorf("unexpected input schema: %+v", tool.InputSchema)
	}
}

func TestToAnthropicTools_EmptyList(t *testing.T) {
	got := ToAnthropicTools(nil)
	if len(got) != 0 {
		t.Errorf("expected 0 tools, got %d", len(got))
	}

	got = ToAnthropicTools([]core.ToolDefinition{})
	if len(got) != 0 {
		t.Errorf("expected 0 tools, got %d", len(got))
	}
}

func TestToAnthropicTools_ProviderDefinedTool(t *testing.T) {
	tools := []core.ToolDefinition{
		{
			Name: "web_search",
			ProviderTool: &core.ProviderDefinedTool{
				ID:   "anthropic.web_search",
				Name: "web_search",
			},
		},
		{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		},
	}
	out := ToAnthropicTools(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(out))
	}
	m, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map for provider tool, got %T", out[0])
	}
	if m["type"] != "web_search_20250305" {
		t.Errorf("unexpected provider tool: %+v", m)
	}
	tool, ok := out[1].(Tool)
	if !ok {
		t.Fatalf("expected Tool, got %T", out[1])
	}
	if tool.Name != "get_weather" {
		t.Errorf("unexpected tool name: %q", tool.Name)
	}
}

func TestToCoreResponse_TextResponse(t *testing.T) {
	resp := &MessagesResponse{
		Content: []Content{{Type: "text", Text: "Hello!"}},
	}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(got.Message.Content))
	}
	tp, ok := got.Message.Content[0].(core.TextPart)
	if !ok || tp.Text != "Hello!" {
		t.Errorf("unexpected content: %+v", got.Message.Content[0])
	}
	if got.Model != "claude-3" {
		t.Errorf("unexpected model: %s", got.Model)
	}
}

func TestToCoreResponse_ThinkingResponse(t *testing.T) {
	resp := &MessagesResponse{
		Content: []Content{{Type: "thinking", Thinking: "Let me think...", Signature: "sig123"}},
	}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(got.Message.Content))
	}
	rp, ok := got.Message.Content[0].(core.ReasoningPart)
	if !ok || rp.Text != "Let me think..." || rp.Signature != "sig123" {
		t.Errorf("unexpected content: %+v", got.Message.Content[0])
	}
}

func TestToCoreResponse_ToolUseResponse(t *testing.T) {
	resp := &MessagesResponse{
		Content: []Content{{Type: "tool_use", ID: "call_1", Name: "get_weather", Input: map[string]any{"city": "Paris"}}},
	}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(got.Message.Content))
	}
	tcp, ok := got.Message.Content[0].(core.ToolCallPart)
	if !ok || tcp.ID != "call_1" || tcp.Name != "get_weather" {
		t.Errorf("unexpected content: %+v", got.Message.Content[0])
	}
	if tcp.Arguments != `{"city":"Paris"}` {
		t.Errorf("unexpected arguments: %s", tcp.Arguments)
	}
}

func TestToCoreResponse_EmptyContent(t *testing.T) {
	resp := &MessagesResponse{Content: []Content{}}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Message.Content) != 0 {
		t.Errorf("expected empty content, got %+v", got.Message.Content)
	}
}

func TestToCoreResponse_WithUsage(t *testing.T) {
	resp := &MessagesResponse{
		Content: []Content{{Type: "text", Text: "OK"}},
		Usage:   &Usage{InputTokens: 10, OutputTokens: 5},
	}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 5 || got.Usage.TotalTokens != 15 {
		t.Errorf("unexpected usage: %+v", got.Usage)
	}
}

func TestToCoreResponse_WithStopReason(t *testing.T) {
	stopReason := "end_turn"
	resp := &MessagesResponse{
		Content:    []Content{{Type: "text", Text: "Done"}},
		StopReason: &stopReason,
	}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.FinishReason != "end_turn" {
		t.Errorf("unexpected finish reason: %s", got.FinishReason)
	}
}

func TestToCoreResponse_NoCandidates(t *testing.T) {
	resp := &MessagesResponse{}
	got, err := ToCoreResponse(resp, "claude-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Message.Role != core.MESSAGE_ROLE_ASSISTANT {
		t.Errorf("unexpected role: %s", got.Message.Role)
	}
	if len(got.Message.Content) != 0 {
		t.Errorf("expected empty content, got %+v", got.Message.Content)
	}
	if got.FinishReason != "" {
		t.Errorf("expected empty finish reason, got %s", got.FinishReason)
	}
	if !reflect.DeepEqual(got.Usage, core.Usage{}) {
		t.Errorf("expected empty usage, got %+v", got.Usage)
	}
}
