package compression

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestEstimateMessageTokens(t *testing.T) {
	m := core.Message{
		Role:    core.MESSAGE_ROLE_USER,
		Content: core.NewTextContent("hello world"),
	}
	if estimateMessageTokens(m) != 3 { // (11+3)/4 = 3
		t.Fatalf("expected 3, got %d", estimateMessageTokens(m))
	}

	// Message with tool call
	m2 := core.Message{
		Role: core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{
			core.ToolCallPart{Name: "search", Arguments: `{"q":"x"}`},
		},
	}
	toks := estimateMessageTokens(m2)
	if toks == 0 {
		t.Fatal("expected non-zero tokens for tool call")
	}
}
