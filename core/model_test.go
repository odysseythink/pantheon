package core

import (
	"testing"
)

func TestRequest_Struct(t *testing.T) {
	maxTokens := 100
	temp := 0.7
	req := Request{
		Messages:     []Message{{Role: RoleUser, Content: []ContentPart{TextPart{Text: "Hi"}}}},
		SystemPrompt: "Be helpful",
		Tools:        []ToolDefinition{{Name: "test", Description: "A test tool"}},
		MaxTokens:    &maxTokens,
		Temperature:  &temp,
		TopP:         &temp,
		StopSequences: []string{"STOP"},
	}
	if req.SystemPrompt != "Be helpful" {
		t.Error("unexpected SystemPrompt")
	}
	if *req.MaxTokens != 100 {
		t.Error("unexpected MaxTokens")
	}
}

func TestResponse_Struct(t *testing.T) {
	resp := Response{
		Message:      Message{Role: RoleAssistant, Content: []ContentPart{TextPart{Text: "Hello"}}},
		FinishReason: "stop",
		Usage:        Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		Model:        "gpt-4",
	}
	if resp.Model != "gpt-4" {
		t.Error("unexpected Model")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("unexpected total tokens: %d", resp.Usage.TotalTokens)
	}
}

func TestUsage_Struct(t *testing.T) {
	u := Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	if u.PromptTokens != 10 || u.CompletionTokens != 5 || u.TotalTokens != 15 {
		t.Errorf("unexpected usage: %+v", u)
	}
}

func TestStreamPart_Struct(t *testing.T) {
	sp := StreamPart{
		Type:           StreamPartTypeTextDelta,
		TextDelta:      "hello",
		ReasoningDelta: "",
		ToolCall:       nil,
		Usage:          &Usage{TotalTokens: 1},
		FinishReason:   "",
	}
	if sp.Type != StreamPartTypeTextDelta {
		t.Error("unexpected type")
	}
	if sp.TextDelta != "hello" {
		t.Error("unexpected TextDelta")
	}
}

func TestObjectRequest_Struct(t *testing.T) {
	req := ObjectRequest{
		Messages:     []Message{{Role: RoleUser, Content: []ContentPart{TextPart{Text: "Hi"}}}},
		SystemPrompt: "Be helpful",
		Schema:       &Schema{Type: "object"},
		Mode:         ObjectModeAuto,
	}
	if req.Mode != ObjectModeAuto {
		t.Errorf("unexpected mode: %s", req.Mode)
	}
}

func TestObjectResponse_Struct(t *testing.T) {
	resp := ObjectResponse{
		Object:       map[string]any{"key": "value"},
		FinishReason: "stop",
		Usage:        Usage{TotalTokens: 5},
		Model:        "gpt-4",
	}
	if resp.Object["key"] != "value" {
		t.Error("unexpected object")
	}
}

func TestObjectStreamPart_Struct(t *testing.T) {
	osp := ObjectStreamPart{
		Type:         ObjectStreamPartTypeObject,
		TextDelta:    "",
		Object:       map[string]any{"key": "value"},
		FinishReason: "stop",
		Usage:        &Usage{TotalTokens: 1},
	}
	if osp.Type != ObjectStreamPartTypeObject {
		t.Error("unexpected type")
	}
}

func TestResponseFormat_Struct(t *testing.T) {
	rf := ResponseFormat{
		Type:       ResponseFormatTypeJSONSchema,
		JSONSchema: &Schema{Type: "object"},
	}
	if rf.Type != ResponseFormatTypeJSONSchema {
		t.Errorf("unexpected type: %s", rf.Type)
	}
}

func TestConstants(t *testing.T) {
	if StreamPartTypeTextDelta != "text_delta" {
		t.Error("unexpected StreamPartTypeTextDelta")
	}
	if StreamPartTypeReasoningDelta != "reasoning_delta" {
		t.Error("unexpected StreamPartTypeReasoningDelta")
	}
	if StreamPartTypeToolCall != "tool_call" {
		t.Error("unexpected StreamPartTypeToolCall")
	}
	if StreamPartTypeUsage != "usage" {
		t.Error("unexpected StreamPartTypeUsage")
	}
	if StreamPartTypeFinish != "finish" {
		t.Error("unexpected StreamPartTypeFinish")
	}
	if ObjectStreamPartTypeTextDelta != "text_delta" {
		t.Error("unexpected ObjectStreamPartTypeTextDelta")
	}
	if ObjectStreamPartTypeObject != "object" {
		t.Error("unexpected ObjectStreamPartTypeObject")
	}
	if ObjectStreamPartTypeUsage != "usage" {
		t.Error("unexpected ObjectStreamPartTypeUsage")
	}
	if ObjectStreamPartTypeFinish != "finish" {
		t.Error("unexpected ObjectStreamPartTypeFinish")
	}
	if ResponseFormatTypeText != "text" {
		t.Error("unexpected ResponseFormatTypeText")
	}
	if ResponseFormatTypeJSON != "json" {
		t.Error("unexpected ResponseFormatTypeJSON")
	}
	if ResponseFormatTypeJSONSchema != "json_schema" {
		t.Error("unexpected ResponseFormatTypeJSONSchema")
	}
	if ObjectModeAuto != "auto" {
		t.Error("unexpected ObjectModeAuto")
	}
	if ObjectModeJSON != "json" {
		t.Error("unexpected ObjectModeJSON")
	}
	if ObjectModeTool != "tool" {
		t.Error("unexpected ObjectModeTool")
	}
	if ObjectModeText != "text" {
		t.Error("unexpected ObjectModeText")
	}
}
