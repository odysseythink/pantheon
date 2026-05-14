package kimi

import (
	"encoding/json"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestBuildRequestBody(t *testing.T) {
	temp := 0.7
	topP := 0.9
	maxTokens := 1024

	req := &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}},
		},
		SystemPrompt:   "sys",
		MaxTokens:      &maxTokens,
		Temperature:    &temp,
		TopP:           &topP,
		StopSequences:  []string{"stop1"},
		ResponseFormat: &core.ResponseFormat{Type: core.ResponseFormatTypeJSON},
		Tools: []core.ToolDefinition{
			{Name: "test_tool", Description: "A test tool", Parameters: &core.Schema{Type: "object"}},
		},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeAuto},
	}
	opts := ProviderOptions{
		PromptCacheKey: "cache-key-123",
	}

	body, err := buildRequestBody("kimi-test", req, opts)
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	if body["model"] != "kimi-test" {
		t.Errorf("model = %v, want kimi-test", body["model"])
	}
	if body["max_tokens"] != 1024 {
		t.Errorf("max_tokens = %v, want 1024", body["max_tokens"])
	}
	if body["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7", body["temperature"])
	}
	if body["top_p"] != 0.9 {
		t.Errorf("top_p = %v, want 0.9", body["top_p"])
	}
	if body["prompt_cache_key"] != "cache-key-123" {
		t.Errorf("prompt_cache_key = %v, want cache-key-123", body["prompt_cache_key"])
	}

	msgs, ok := body["messages"].([]Message)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want 2 messages", body["messages"])
	}
	if msgs[0].Role != "system" || msgs[0].Content != "sys" {
		t.Errorf("first message = %+v, want system/sys", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hello" {
		t.Errorf("second message = %+v, want user/hello", msgs[1])
	}
}

func TestBuildRequestBody_DefaultMaxTokens(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hi"}}},
		},
	}
	body, err := buildRequestBody("kimi-test", req, ProviderOptions{})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	if body["max_tokens"] != 32000 {
		t.Errorf("max_tokens = %v, want 32000", body["max_tokens"])
	}
}

func TestBuildRequestBody_Thinking(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hi"}}},
		},
	}
	opts := ProviderOptions{
		Thinking: &ThinkingConfig{Type: "enabled", Keep: "all"},
	}
	body, err := buildRequestBody("kimi-test", req, opts)
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", body["reasoning_effort"])
	}
	extraBody, ok := body["extra_body"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body type = %T, want map[string]any", body["extra_body"])
	}
	thinking, ok := extraBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking type = %T, want map[string]any", extraBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
	if thinking["keep"] != "all" {
		t.Errorf("thinking.keep = %v, want all", thinking["keep"])
	}
}

func TestBuildRequestBody_ExtraBodyWithThinking(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hi"}}},
		},
	}
	opts := ProviderOptions{
		Thinking:  &ThinkingConfig{Type: "enabled", Keep: "all"},
		ExtraBody: map[string]any{"custom": "value", "thinking": map[string]any{"budget_tokens": 1000}},
	}
	body, err := buildRequestBody("kimi-test", req, opts)
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", body["reasoning_effort"])
	}
	extraBody, ok := body["extra_body"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body type = %T, want map[string]any", body["extra_body"])
	}
	if extraBody["custom"] != "value" {
		t.Errorf("extra_body.custom = %v, want value", extraBody["custom"])
	}
	thinking, ok := extraBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking type = %T, want map[string]any", extraBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
	if thinking["keep"] != "all" {
		t.Errorf("thinking.keep = %v, want all", thinking["keep"])
	}
	if thinking["budget_tokens"] != 1000 {
		t.Errorf("thinking.budget_tokens = %v, want 1000", thinking["budget_tokens"])
	}
}

func TestBuildRequestBody_ExtraBodyWithoutThinking(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hi"}}},
		},
	}
	opts := ProviderOptions{
		ExtraBody: map[string]any{"custom": "value"},
	}
	body, err := buildRequestBody("kimi-test", req, opts)
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}
	extraBody, ok := body["extra_body"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body type = %T, want map[string]any", body["extra_body"])
	}
	if extraBody["custom"] != "value" {
		t.Errorf("extra_body.custom = %v, want value", extraBody["custom"])
	}
}

func TestToKimiMessages_SystemPrompt(t *testing.T) {
	msgs, err := toKimiMessages([]core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}},
	}, "system-instruction")
	if err != nil {
		t.Fatalf("toKimiMessages error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "system-instruction" {
		t.Errorf("first message = %+v", msgs[0])
	}
}

func TestToKimiMessage_System(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role:    core.MESSAGE_ROLE_SYSTEM,
		Content: []core.ContentParter{core.TextPart{Text: "sys"}},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "system" || msg.Content != "sys" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestToKimiMessage_UserText(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role:    core.MESSAGE_ROLE_USER,
		Content: []core.ContentParter{core.TextPart{Text: "hello"}},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "user" || msg.Content != "hello" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestToKimiMessage_UserMultimodal(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role: core.MESSAGE_ROLE_USER,
		Content: []core.ContentParter{
			core.TextPart{Text: "look"},
			core.ImagePart{URL: "http://example.com/img.png", Detail: "high"},
		},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	parts, ok := msg.Content.([]ContentParter)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %v", msg.Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "look" {
		t.Errorf("first part = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL.URL != "http://example.com/img.png" {
		t.Errorf("second part = %+v", parts[1])
	}
}

func TestToKimiMessage_AssistantText(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role:    core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{core.TextPart{Text: "hi there"}},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "assistant" || msg.Content != "hi there" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestToKimiMessage_AssistantWithToolCalls(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role: core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{
			core.TextPart{Text: "   "},
			core.ToolCallPart{ID: "call_1", Name: "foo", Arguments: `{}`},
		},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "assistant" {
		t.Errorf("role = %s, want assistant", msg.Role)
	}
	if msg.Content != nil {
		t.Errorf("content = %v, want nil", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Name != "foo" {
		t.Errorf("tool_call = %+v", msg.ToolCalls[0])
	}
}

func TestToKimiMessage_AssistantWithReasoning(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role: core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{
			core.ReasoningPart{Text: "Let me think..."},
			core.TextPart{Text: "Result"},
		},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.ReasoningContent != "Let me think..." {
		t.Errorf("reasoning_content = %v", msg.ReasoningContent)
	}
	if msg.Content != "Result" {
		t.Errorf("content = %v", msg.Content)
	}
}

func TestToKimiMessage_AssistantWithReasoningAndToolCalls(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role: core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{
			core.ReasoningPart{Text: "Let me think..."},
			core.TextPart{Text: "   "},
			core.ToolCallPart{ID: "call_1", Name: "foo", Arguments: `{}`},
		},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "assistant" {
		t.Errorf("role = %s, want assistant", msg.Role)
	}
	if msg.ReasoningContent != "Let me think..." {
		t.Errorf("reasoning_content = %v, want 'Let me think...'", msg.ReasoningContent)
	}
	if msg.Content != nil {
		t.Errorf("content = %v, want nil", msg.Content)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Name != "foo" {
		t.Errorf("tool_call = %+v", msg.ToolCalls[0])
	}
}

func TestToKimiMessage_ToolResult(t *testing.T) {
	msg, err := toKimiMessage(core.Message{
		Role: core.MESSAGE_ROLE_TOOL,
		Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "call_1", Name: "foo", Content: []core.ContentParter{core.TextPart{Text: "result"}}},
		},
	})
	if err != nil {
		t.Fatalf("toKimiMessage error: %v", err)
	}
	if msg.Role != "tool" || msg.ToolCallID != "call_1" || msg.Content != "result" {
		t.Errorf("msg = %+v", msg)
	}
}

func TestContentToString(t *testing.T) {
	parts := []core.ContentParter{
		core.TextPart{Text: "a"},
		core.ToolResultPart{Content: []core.ContentParter{core.TextPart{Text: "b"}}},
	}
	if s := contentToString(parts); s != "a\nb" {
		t.Errorf("contentToString = %q", s)
	}
}

func TestToolResultCallID(t *testing.T) {
	parts := []core.ContentParter{
		core.ToolResultPart{ToolCallID: "id-123"},
	}
	if id := toolResultCallID(parts); id != "id-123" {
		t.Errorf("toolResultCallID = %q", id)
	}
}

func TestJoinTexts(t *testing.T) {
	if s := joinTexts([]string{"a", "b", "c"}); s != "a\nb\nc" {
		t.Errorf("joinTexts = %q", s)
	}
}

func TestIsEffectivelyEmpty(t *testing.T) {
	if !isEffectivelyEmpty([]string{"", "   ", "\t\n"}) {
		t.Error("expected empty")
	}
	if isEffectivelyEmpty([]string{"", "x"}) {
		t.Error("expected not empty")
	}
}

func TestToKimiTool_Builtin(t *testing.T) {
	tool, err := toKimiTool(core.ToolDefinition{Name: "$web_search", Description: "search", Parameters: &core.Schema{Type: "object"}})
	if err != nil {
		t.Fatalf("toKimiTool error: %v", err)
	}
	if tool.Type != "builtin_function" {
		t.Errorf("type = %s", tool.Type)
	}
	if tool.Function.Name != "$web_search" {
		t.Errorf("name = %s", tool.Function.Name)
	}
	if tool.Function.Description != "" {
		t.Errorf("description should be empty for builtin")
	}
	if tool.Function.Parameters != nil {
		t.Errorf("parameters should be nil for builtin")
	}
}

func TestToKimiTool_Function(t *testing.T) {
	tool, err := toKimiTool(core.ToolDefinition{Name: "get_weather", Description: "Get weather", Parameters: &core.Schema{Type: "object"}})
	if err != nil {
		t.Fatalf("toKimiTool error: %v", err)
	}
	if tool.Type != "function" {
		t.Errorf("type = %s", tool.Type)
	}
	if tool.Function.Name != "get_weather" {
		t.Errorf("name = %s", tool.Function.Name)
	}
	if tool.Function.Description != "Get weather" {
		t.Errorf("description = %s", tool.Function.Description)
	}
	if tool.Function.Parameters == nil {
		t.Error("parameters should not be nil")
	}
}

func TestToKimiToolChoice(t *testing.T) {
	if v := toKimiToolChoice(core.ToolChoice{Mode: core.ToolChoiceModeAuto}); v != "auto" {
		t.Errorf("auto = %v", v)
	}
	if v := toKimiToolChoice(core.ToolChoice{Mode: core.ToolChoiceModeNone}); v != "none" {
		t.Errorf("none = %v", v)
	}
	v := toKimiToolChoice(core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "foo"})
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("required = %T", v)
	}
	if m["type"] != "function" {
		t.Errorf("type = %v", m["type"])
	}
}

func TestEnsurePropertyTypes(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{},
			"age": map[string]any{
				"type": "integer",
			},
		},
	}
	result := ensurePropertyTypes(schema).(map[string]any)
	props := result["properties"].(map[string]any)
	nameProp := props["name"].(map[string]any)
	if nameProp["type"] != "string" {
		t.Errorf("name.type = %v", nameProp["type"])
	}
	ageProp := props["age"].(map[string]any)
	if ageProp["type"] != "integer" {
		t.Errorf("age.type = %v", ageProp["type"])
	}
}

func TestEnsurePropertyTypes_NestedItems(t *testing.T) {
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"properties": map[string]any{
				"nested": map[string]any{},
			},
		},
	}
	result := ensurePropertyTypes(schema).(map[string]any)
	items := result["items"].(map[string]any)
	props := items["properties"].(map[string]any)
	nested := props["nested"].(map[string]any)
	if nested["type"] != "string" {
		t.Errorf("nested.type = %v", nested["type"])
	}
}

func TestThinkingToReasoningEffort(t *testing.T) {
	if v := thinkingToReasoningEffort("enabled"); v != "high" {
		t.Errorf("enabled = %q", v)
	}
	if v := thinkingToReasoningEffort("disabled"); v != "" {
		t.Errorf("disabled = %q", v)
	}
	if v := thinkingToReasoningEffort("unknown"); v != "" {
		t.Errorf("unknown = %q", v)
	}
}

func TestEnsurePropertyTypes_FromSchema(t *testing.T) {
	schema := &core.Schema{
		Type: "object",
		Properties: map[string]*core.Schema{
			"name": {Description: "A name"},
			"age":  {Type: "integer"},
		},
	}
	data, _ := json.Marshal(schema)
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	result := ensurePropertyTypes(m).(map[string]any)
	props := result["properties"].(map[string]any)
	nameProp := props["name"].(map[string]any)
	if nameProp["type"] != "string" {
		t.Errorf("name.type = %v", nameProp["type"])
	}
}
