package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/types"
)

func TestChatCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "Hello!"},
				FinishReason: ptr("stop"),
			}},
			Usage: &Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	resp, err := c.ChatCompletion(context.Background(), "gpt-4", &core.Request{
		Messages:     []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
		SystemPrompt: "Be helpful",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello!" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
}

func TestChatCompletion_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid key"))
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-bad")
	_, err := c.ChatCompletion(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChatCompletion_WithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if len(req.Tools) != 1 {
			t.Errorf("expected 1 tool, got %d", len(req.Tools))
		}
		if req.ToolChoice == nil {
			t.Error("expected tool choice")
		}
		resp := ChatCompletionResponse{
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
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	resp, err := c.ChatCompletion(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		}},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "get_weather"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
}

func TestChatCompletion_WithResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if req.ResponseFormat == nil {
			t.Error("expected response format")
		}
		resp := ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: `{"result":42}`},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	_, err := c.ChatCompletion(context.Background(), "gpt-4", &core.Request{
		Messages:       []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Generate JSON"}}}},
		ResponseFormat: &core.ResponseFormat{Type: core.ResponseFormatTypeJSON},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChatCompletion_CustomPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/path" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "OK"},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.ChatCompletionPath = "/custom/path"
	_, err := c.ChatCompletion(context.Background(), "gpt-4", &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestToOpenAIToolChoice(t *testing.T) {
	tests := []struct {
		name string
		tc   core.ToolChoice
		want string
	}{
		{"auto", core.ToolChoice{Mode: core.ToolChoiceModeAuto}, "auto"},
		{"none", core.ToolChoice{Mode: core.ToolChoiceModeNone}, "none"},
		{"required no name", core.ToolChoice{Mode: core.ToolChoiceModeRequired}, "required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toOpenAIToolChoice(tt.tc)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("required with name", func(t *testing.T) {
		got := toOpenAIToolChoice(core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "foo"})
		m, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", got)
		}
		if m["type"] != "function" {
			t.Errorf("unexpected type: %v", m["type"])
		}
	})

	t.Run("default", func(t *testing.T) {
		got := toOpenAIToolChoice(core.ToolChoice{})
		if got != "auto" {
			t.Errorf("expected auto for empty, got %v", got)
		}
	})
}

func TestToOpenAIResponseFormat(t *testing.T) {
	tests := []struct {
		name string
		rf   *core.ResponseFormat
		want string
	}{
		{"text", &core.ResponseFormat{Type: core.ResponseFormatTypeText}, "text"},
		{"json", &core.ResponseFormat{Type: core.ResponseFormatTypeJSON}, "json_object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toOpenAIResponseFormat(tt.rf)
			m, ok := got.(map[string]string)
			if !ok {
				t.Fatalf("expected map[string]string, got %T", got)
			}
			if m["type"] != tt.want {
				t.Errorf("got type %q, want %q", m["type"], tt.want)
			}
		})
	}

	t.Run("json_schema", func(t *testing.T) {
		got := toOpenAIResponseFormat(&core.ResponseFormat{
			Type:       core.ResponseFormatTypeJSONSchema,
			JSONSchema: &core.Schema{Type: "object"},
		})
		m, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any, got %T", got)
		}
		if m["type"] != "json_schema" {
			t.Errorf("got type %v, want json_schema", m["type"])
		}
	})
}

// ProviderOptions（ExtraBody / ReasoningEffort / User）必须落到请求体顶层：
// 自部署 vLLM 的思考开关走 chat_template_kwargs.enable_thinking，网关特定
// 参数全靠 ExtraBody 透传（2026-10-20 odyBox 思考过程不上屏案请求侧缺口）。
func TestChatCompletion_ProviderOptionsApplied(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		resp := ChatCompletionResponse{
			Model: "kb-small",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "81"},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	effort := "high"
	user := "odybox"
	po := ProviderOptions{
		ReasoningEffort: &effort,
		User:            &user,
		ExtraBody: map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": true},
		},
	}
	coreReq := &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "9*9=?"}}}},
	}
	coreReq.ProviderOptions = core.ProviderOptions{}
	coreReq.ProviderOptions.Set(Name, po)

	c := NewClient(server.URL, "sk-test")
	if _, err := c.ChatCompletion(context.Background(), "kb-small", coreReq); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", gotBody["reasoning_effort"])
	}
	if gotBody["user"] != "odybox" {
		t.Errorf("user = %v, want odybox", gotBody["user"])
	}
	ctk, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || ctk["enable_thinking"] != true {
		t.Errorf("chat_template_kwargs.enable_thinking = %v, want true (body: %v)", gotBody["chat_template_kwargs"], gotBody)
	}
	if gotBody["model"] != "kb-small" {
		t.Errorf("model = %v, want kb-small", gotBody["model"])
	}
}

// 流式路径同样要应用 ProviderOptions（两条发送路径共用同一构造逻辑）。
func TestChatCompletionStream_ProviderOptionsApplied(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"kb-small","choices":[{"index":0,"delta":{"content":"81"},"finish_reason":"stop"}]}

data: [DONE]

`)
	}))
	defer server.Close()

	po := ProviderOptions{
		ExtraBody: map[string]any{
			"chat_template_kwargs": map[string]any{"enable_thinking": true},
		},
	}
	coreReq := &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "9*9=?"}}}},
	}
	coreReq.ProviderOptions = core.ProviderOptions{}
	coreReq.ProviderOptions.Set(Name, po)

	c := NewClient(server.URL, "sk-test")
	for _, err := range c.ChatCompletionStream(context.Background(), "kb-small", coreReq) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}

	ctk, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || ctk["enable_thinking"] != true {
		t.Errorf("stream chat_template_kwargs.enable_thinking = %v, want true (body: %v)", gotBody["chat_template_kwargs"], gotBody)
	}
}
