package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/odysseythink/ai/core"
)

// Generate creates a JSON Schema from a Go type.
// This delegates to core.GenerateSchema.
func Generate(t reflect.Type) *core.Schema {
	return core.GenerateSchema(t)
}

// ParsePartialJSON attempts to parse JSON, tolerating common LLM truncation issues.
func ParsePartialJSON(text string, schema *core.Schema) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		return obj, nil
	}

	fixed := text
	openBraces := strings.Count(fixed, "{") - strings.Count(fixed, "}")
	openBrackets := strings.Count(fixed, "[") - strings.Count(fixed, "]")
	for i := 0; i < openBraces; i++ {
		fixed += "}"
	}
	for i := 0; i < openBrackets; i++ {
		fixed += "]"
	}
	fixed = strings.TrimSpace(fixed)
	fixed = strings.TrimSuffix(fixed, ",")
	if !strings.HasSuffix(fixed, "}") && !strings.HasSuffix(fixed, "]") {
		fixed += "}"
	}

	if err := json.Unmarshal([]byte(fixed), &obj); err != nil {
		return nil, fmt.Errorf("parse partial JSON: %w", err)
	}
	return obj, nil
}

// RepairToolCall attempts to fix malformed tool call arguments.
// For now, it validates the JSON and returns it unchanged if valid.
// More sophisticated repair can be added later.
func RepairToolCall(toolCall *core.ToolCallPart, schema *core.Schema) (*core.ToolCallPart, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(toolCall.Arguments), &obj); err != nil {
		return nil, fmt.Errorf("unable to repair tool call arguments: %w", err)
	}
	return &core.ToolCallPart{
		ID:        toolCall.ID,
		Name:      toolCall.Name,
		Arguments: toolCall.Arguments,
	}, nil
}
