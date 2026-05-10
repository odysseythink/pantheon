package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/ai/core"
)

func TestAnthropicGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := MessagesResponse{
			ID:   "msg_1",
			Type: "message",
			Role: "assistant",
			Content: []Content{
				{Type: "text", Text: "Hello from Claude!"},
			},
			Model:      "claude-3-opus",
			StopReason: ptr("end_turn"),
			Usage:      &Usage{InputTokens: 5, OutputTokens: 4},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "claude-3-opus")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello from Claude!" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
}

func ptr(s string) *string { return &s }
