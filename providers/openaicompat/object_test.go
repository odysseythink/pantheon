package openaicompat

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestExtractObjectResponse_FromText(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{core.TextPart{Text: `{"name":"test","value":42}`}},
		},
		FinishReason: "stop",
		Usage:        core.Usage{TotalTokens: 10},
		Model:        "gpt-4",
	}
	objResp, err := ExtractObjectResponse(resp, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if objResp.Object == nil {
		t.Fatal("expected object")
	}
	if objResp.Object["name"] != "test" {
		t.Errorf("unexpected name: %+v", objResp.Object)
	}
	if objResp.Object["value"] != float64(42) {
		t.Errorf("unexpected value: %+v", objResp.Object)
	}
	if objResp.Model != "gpt-4" {
		t.Errorf("unexpected model: %s", objResp.Model)
	}
}

func TestExtractObjectResponse_FromToolCall(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role: core.RoleAssistant,
			Content: []core.ContentPart{core.ToolCallPart{
				ID:        "call_1",
				Name:      "generate_object",
				Arguments: `{"result":true}`,
			}},
		},
		FinishReason: "stop",
	}
	objResp, err := ExtractObjectResponse(resp, "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if objResp.Object == nil {
		t.Fatal("expected object")
	}
	if objResp.Object["result"] != true {
		t.Errorf("unexpected result: %+v", objResp.Object)
	}
}

func TestExtractObjectResponse_InvalidJSON(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{core.TextPart{Text: `not json`}},
		},
	}
	_, err := ExtractObjectResponse(resp, "gpt-4")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractObjectResponse_NoContent(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{},
		},
	}
	_, err := ExtractObjectResponse(resp, "gpt-4")
	if err != core.ErrNoObjectGenerated {
		t.Fatalf("expected ErrNoObjectGenerated, got %v", err)
	}
}
