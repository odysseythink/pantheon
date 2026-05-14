package google

import (
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToGeminiMessages(t *testing.T) {
	tests := []struct {
		name    string
		msgs    []core.Message
		want    []Content
		wantErr bool
	}{
		{
			name: "user text",
			msgs: []core.Message{
				{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
			},
			want: []Content{
				{Role: "user", Parts: []Part{{Text: "Hello"}}},
			},
		},
		{
			name: "assistant text",
			msgs: []core.Message{
				{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hi there"}}},
			},
			want: []Content{
				{Role: "model", Parts: []Part{{Text: "Hi there"}}},
			},
		},
		{
			name: "tool role",
			msgs: []core.Message{
				{Role: core.MESSAGE_ROLE_TOOL, Content: []core.ContentParter{core.TextPart{Text: "Result"}}},
			},
			want: []Content{
				{Role: "user", Parts: []Part{{Text: "Result"}}},
			},
		},
		{
			name: "system prompt maps to user",
			msgs: []core.Message{
				{Role: core.MESSAGE_ROLE_SYSTEM, Content: []core.ContentParter{core.TextPart{Text: "You are helpful"}}},
			},
			want: []Content{
				{Role: "user", Parts: []Part{{Text: "You are helpful"}}},
			},
		},
		{
			name: "multiple messages",
			msgs: []core.Message{
				{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
				{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}},
				{Role: core.MESSAGE_ROLE_TOOL, Content: []core.ContentParter{core.TextPart{Text: "Done"}}},
			},
			want: []Content{
				{Role: "user", Parts: []Part{{Text: "Hello"}}},
				{Role: "model", Parts: []Part{{Text: "Hi"}}},
				{Role: "user", Parts: []Part{{Text: "Done"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toGeminiMessages(tt.msgs)
			if (err != nil) != tt.wantErr {
				t.Fatalf("toGeminiMessages() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toGeminiMessages() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToGeminiParts(t *testing.T) {
	tests := []struct {
		name    string
		parts   []core.ContentParter
		want    []Part
		wantErr string
	}{
		{
			name:  "text",
			parts: []core.ContentParter{core.TextPart{Text: "hello"}},
			want:  []Part{{Text: "hello"}},
		},
		{
			name:  "image with data",
			parts: []core.ContentParter{core.ImagePart{Data: []byte("img"), MIMEType: "image/png"}},
			want:  []Part{{InlineData: &Blob{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("img"))}}},
		},
		{
			name:    "image URL error",
			parts:   []core.ContentParter{core.ImagePart{URL: "http://example.com/img.png", MIMEType: "image/png"}},
			wantErr: "google: image URLs must be fetched first",
		},
		{
			name:  "audio with data",
			parts: []core.ContentParter{core.AudioPart{Data: []byte("audio"), MIMEType: "audio/mp3"}},
			want:  []Part{{InlineData: &Blob{MimeType: "audio/mp3", Data: base64.StdEncoding.EncodeToString([]byte("audio"))}}},
		},
		{
			name:    "audio URL error",
			parts:   []core.ContentParter{core.AudioPart{URL: "http://example.com/audio.mp3", MIMEType: "audio/mp3"}},
			wantErr: "google: audio URLs must be fetched first",
		},
		{
			name:  "document with data",
			parts: []core.ContentParter{core.DocumentPart{Data: []byte("doc"), MIMEType: "application/pdf"}},
			want:  []Part{{InlineData: &Blob{MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte("doc"))}}},
		},
		{
			name:  "tool call",
			parts: []core.ContentParter{core.ToolCallPart{ID: "1", Name: "get_weather", Arguments: `{"city":"Paris"}`}},
			want:  []Part{{FunctionCall: &FunctionCall{Name: "get_weather", Args: map[string]interface{}{"city": "Paris"}}}},
		},
		{
			name:  "tool result",
			parts: []core.ContentParter{core.ToolResultPart{ToolCallID: "1", Name: "get_weather", Content: []core.ContentParter{core.TextPart{Text: "sunny"}}}},
			want:  []Part{{FunctionResponse: &FunctionResponse{Name: "get_weather", Response: map[string]interface{}{"result": "sunny"}}}}},
		{
			name:    "unsupported part error",
			parts:   []core.ContentParter{core.ReasoningPart{Text: "thinking"}},
			wantErr: "google: unsupported content part: core.ReasoningPart",
		},
		{
			name:    "invalid JSON arguments error",
			parts:   []core.ContentParter{core.ToolCallPart{ID: "1", Name: "get_weather", Arguments: `not json`}},
			wantErr: "google: invalid tool call arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toGeminiParts(tt.parts)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr && !contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toGeminiParts() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) > 0 && indexOfSubstring(s, substr) >= 0)
}

func indexOfSubstring(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func TestContentToString(t *testing.T) {
	tests := []struct {
		name  string
		parts []core.ContentParter
		want  string
	}{
		{
			name:  "single text part",
			parts: []core.ContentParter{core.TextPart{Text: "hello"}},
			want:  "hello",
		},
		{
			name:  "multiple text parts",
			parts: []core.ContentParter{core.TextPart{Text: "hello"}, core.TextPart{Text: "world"}},
			want:  "hello\nworld",
		},
		{
			name:  "empty",
			parts: []core.ContentParter{},
			want:  "",
		},
		{
			name:  "ignores non-text parts",
			parts: []core.ContentParter{core.ImagePart{Data: []byte("img")}, core.TextPart{Text: "hi"}},
			want:  "hi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contentToString(tt.parts)
			if got != tt.want {
				t.Errorf("contentToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToGeminiTools(t *testing.T) {
	schema := &core.Schema{Type: "object"}
	tests := []struct {
		name  string
		tools []core.ToolDefinition
		want  []Tool
	}{
		{
			name: "normal conversion",
			tools: []core.ToolDefinition{
				{Name: "get_weather", Description: "Get weather", Parameters: schema},
			},
			want: []Tool{
				{FunctionDeclarations: []FunctionDeclaration{{Name: "get_weather", Description: "Get weather", Parameters: schema}}},
			},
		},
		{
			name:  "empty list",
			tools: []core.ToolDefinition{},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toGeminiTools(tt.tools)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toGeminiTools() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToGeminiToolConfig(t *testing.T) {
	tests := []struct {
		name string
		tc   core.ToolChoice
		want *ToolConfig
	}{
		{
			name: "auto",
			tc:   core.ToolChoice{Mode: core.ToolChoiceModeAuto},
			want: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "AUTO"}},
		},
		{
			name: "none",
			tc:   core.ToolChoice{Mode: core.ToolChoiceModeNone},
			want: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "NONE"}},
		},
		{
			name: "required with name",
			tc:   core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "get_weather"},
			want: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{"get_weather"}}},
		},
		{
			name: "required without name",
			tc:   core.ToolChoice{Mode: core.ToolChoiceModeRequired},
			want: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "ANY"}},
		},
		{
			name: "empty",
			tc:   core.ToolChoice{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toGeminiToolConfig(tt.tc)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toGeminiToolConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestToCoreResponse(t *testing.T) {
	tests := []struct {
		name    string
		resp    *GenerateContentResponse
		model   string
		want    *core.Response
		wantErr string
	}{
		{
			name: "text response",
			resp: &GenerateContentResponse{
				Candidates: []Candidate{{
					Content:      Content{Parts: []Part{{Text: "Hello"}}},
					FinishReason: "STOP",
				}},
			},
			model: "gemini-pro",
			want: &core.Response{
				Message:      core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hello"}}},
				FinishReason: "STOP",
				Model:        "gemini-pro",
			},
		},
		{
			name: "function call response",
			resp: &GenerateContentResponse{
				Candidates: []Candidate{{
					Content: Content{Parts: []Part{{
						FunctionCall: &FunctionCall{Name: "get_weather", Args: map[string]interface{}{"city": "Paris"}},
					}}},
					FinishReason: "STOP",
				}},
			},
			model: "gemini-pro",
			want: &core.Response{
				Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
					core.ToolCallPart{ID: "get_weather", Name: "get_weather", Arguments: `{"city":"Paris"}`},
				}},
				FinishReason: "STOP",
				Model:        "gemini-pro",
			},
		},
		{
			name: "empty candidates with block reason",
			resp: &GenerateContentResponse{
				Candidates:     []Candidate{},
				PromptFeedback: &PromptFeedback{BlockReason: "SAFETY"},
			},
			model:   "gemini-pro",
			wantErr: "prompt blocked: SAFETY",
		},
		{
			name:    "empty candidates without block reason",
			resp:    &GenerateContentResponse{Candidates: []Candidate{}},
			model:   "gemini-pro",
			wantErr: "no candidates in response",
		},
		{
			name: "with usage metadata",
			resp: &GenerateContentResponse{
				Candidates: []Candidate{{
					Content:      Content{Parts: []Part{{Text: "Hi"}}},
					FinishReason: "STOP",
				}},
				UsageMetadata: &UsageMetadata{
					PromptTokenCount:     5,
					CandidatesTokenCount: 4,
					TotalTokenCount:      9,
				},
			},
			model: "gemini-pro",
			want: &core.Response{
				Message:      core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}},
				FinishReason: "STOP",
				Usage:        core.Usage{PromptTokens: 5, CompletionTokens: 4, TotalTokens: 9},
				Model:        "gemini-pro",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toCoreResponse(tt.resp, tt.model)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("error = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("toCoreResponse() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
