package schema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// jsonMarshal is a variable alias for json.Marshal, allowing tests to inject failures.
var jsonMarshal = json.Marshal

// Generate creates a JSON Schema from a Go type.
// This delegates to core.GenerateSchema.
func Generate(t reflect.Type) *core.Schema {
	return core.GenerateSchema(t)
}

// ParsePartialJSON attempts to parse JSON, tolerating common LLM truncation issues.
// The schema parameter is reserved for future schema-aware validation.
func ParsePartialJSON(text string, schema *core.Schema) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err == nil {
		return obj, nil
	}

	fixed := text
	// Close unclosed braces/brackets
	openBraces := strings.Count(fixed, "{") - strings.Count(fixed, "}")
	openBrackets := strings.Count(fixed, "[") - strings.Count(fixed, "]")
	for i := 0; i < openBraces; i++ {
		fixed += "}"
	}
	for i := 0; i < openBrackets; i++ {
		fixed += "]"
	}
	fixed = strings.TrimSpace(fixed)
	// Remove trailing comma before closing brace/bracket
	fixed = removeTrailingComma(fixed)
	if !strings.HasSuffix(fixed, "}") && !strings.HasSuffix(fixed, "]") {
		fixed += "}"
	}

	if err := json.Unmarshal([]byte(fixed), &obj); err != nil {
		return nil, fmt.Errorf("parse partial JSON: %w", err)
	}
	return obj, nil
}

// removeTrailingComma removes a comma that appears immediately before the final } or ].
func removeTrailingComma(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	last := s[len(s)-1]
	if last != '}' && last != ']' {
		return s
	}
	// Walk backwards to find the matching opening brace/bracket
	depth := 1
	for i := len(s) - 2; i >= 0 && depth > 0; i-- {
		switch s[i] {
		case '}', ']':
			depth++
		case '{', '[':
			depth--
			if depth == 0 && i > 0 && s[i-1] == ',' {
				// Remove comma before opening brace/bracket
				return s[:i-1] + s[i:]
			}
		}
	}
	return s
}

// RepairToolCall attempts to fix malformed tool call arguments.
// It tries ParsePartialJSON heuristics and validates the result.
func RepairToolCall(toolCall *core.ToolCallPart, schema *core.Schema) (*core.ToolCallPart, error) {
	args := toolCall.Arguments

	// Try parsing to see if it's already valid
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err == nil {
		return &core.ToolCallPart{
			ID:        toolCall.ID,
			Name:      toolCall.Name,
			Arguments: args,
		}, nil
	}

	// Try partial JSON repair
	repaired, err := ParsePartialJSON(args, schema)
	if err != nil {
		return nil, fmt.Errorf("unable to repair tool call arguments: %w", err)
	}

	repairedJSON, err := jsonMarshal(repaired)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal repaired arguments: %w", err)
	}

	return &core.ToolCallPart{
		ID:        toolCall.ID,
		Name:      toolCall.Name,
		Arguments: string(repairedJSON),
	}, nil
}
