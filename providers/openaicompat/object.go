package openaicompat

import (
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// ExtractObjectResponse extracts a JSON object from a model response.
// It checks both text content and tool call arguments for parseable JSON.
func ExtractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			if err := json.Unmarshal([]byte(p.Text), &obj); err != nil {
				return nil, fmt.Errorf("parse object: %w", err)
			}
			break
		}
		if p, ok := part.(core.ToolCallPart); ok {
			if err := json.Unmarshal([]byte(p.Arguments), &obj); err != nil {
				return nil, fmt.Errorf("parse tool arguments: %w", err)
			}
			break
		}
	}
	if obj == nil {
		return nil, core.ErrNoObjectGenerated
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}
