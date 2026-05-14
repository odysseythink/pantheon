package kimi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/types"
)

// buildRequestBody constructs the Kimi chat completion request body as map[string]any.
func buildRequestBody(model string, req *core.Request, opts ProviderOptions) (map[string]any, error) {
	messages, err := toKimiMessages(req.Messages, req.SystemPrompt)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    model,
		"messages": messages,
	}

	maxTokens := 32000
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}
	body["max_tokens"] = maxTokens

	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		body["response_format"] = toKimiResponseFormat(req.ResponseFormat)
	}
	if len(req.Tools) > 0 {
		tools := make([]Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tool, err := toKimiTool(t)
			if err != nil {
				return nil, err
			}
			tools = append(tools, tool)
		}
		body["tools"] = tools
		body["tool_choice"] = toKimiToolChoice(req.ToolChoice)
	}
	if opts.PromptCacheKey != "" {
		body["prompt_cache_key"] = opts.PromptCacheKey
	}
	if opts.Thinking != nil {
		body["reasoning_effort"] = thinkingToReasoningEffort(opts.Thinking.Type)
		extraBody := make(map[string]any)
		if opts.ExtraBody != nil {
			for k, v := range opts.ExtraBody {
				extraBody[k] = v
			}
		}
		thinkingMap := map[string]any{
			"type": opts.Thinking.Type,
		}
		if opts.Thinking.Keep != "" {
			thinkingMap["keep"] = opts.Thinking.Keep
		}
		// Deep-merge the thinking sub-dict: generated fields win, but preserve
		// extra fields from ExtraBody["thinking"].
		if existingThinking, ok := extraBody["thinking"].(map[string]any); ok {
			merged := make(map[string]any, len(existingThinking)+len(thinkingMap))
			for k, v := range existingThinking {
				merged[k] = v
			}
			for k, v := range thinkingMap {
				merged[k] = v
			}
			thinkingMap = merged
		}
		extraBody["thinking"] = thinkingMap
		body["extra_body"] = extraBody
	} else if opts.ExtraBody != nil {
		body["extra_body"] = opts.ExtraBody
	}

	return body, nil
}

func toKimiResponseFormat(rf *core.ResponseFormat) any {
	switch rf.Type {
	case core.ResponseFormatTypeJSON:
		return map[string]string{"type": "json_object"}
	case core.ResponseFormatTypeJSONSchema:
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"schema": rf.JSONSchema,
				"strict": true,
			},
		}
	default:
		return map[string]string{"type": "text"}
	}
}

// toKimiMessages converts core.Message slice to Kimi wire format.
func toKimiMessages(msgs []core.Message, systemPrompt string) ([]Message, error) {
	var out []Message
	if systemPrompt != "" {
		out = append(out, Message{Role: "system", Content: systemPrompt})
	}
	for _, m := range msgs {
		km, err := toKimiMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, km)
	}
	return out, nil
}

// toKimiMessage converts a single core.Message to Kimi format.
func toKimiMessage(m core.Message) (Message, error) {
	switch m.Role {
	case core.MESSAGE_ROLE_SYSTEM:
		return Message{Role: "system", Content: contentToString(m.Content)}, nil
	case core.MESSAGE_ROLE_USER:
		content, err := contentToKimi(m.Content)
		if err != nil {
			return Message{}, err
		}
		return Message{Role: "user", Content: content}, nil
	case core.MESSAGE_ROLE_ASSISTANT:
		msg := Message{Role: "assistant"}
		var textParts []string
		var reasoningContent string
		for _, part := range m.Content {
			switch p := part.(type) {
			case core.TextPart:
				textParts = append(textParts, p.Text)
			case core.ReasoningPart:
				reasoningContent += p.Text
			case core.ToolCallPart:
				msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
					ID:   p.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      p.Name,
						Arguments: p.Arguments,
					},
				})
			default:
				return Message{}, fmt.Errorf("kimi: unsupported content part in assistant message: %T", part)
			}
		}
		if reasoningContent != "" {
			msg.ReasoningContent = reasoningContent
		}
		hasToolCalls := len(msg.ToolCalls) > 0
		hasTextParts := len(textParts) > 0
		if hasTextParts && !isEffectivelyEmpty(textParts) {
			msg.Content = joinTexts(textParts)
		} else if hasToolCalls && (!hasTextParts || isEffectivelyEmpty(textParts)) {
			msg.Content = nil
		}
		return msg, nil
	case core.MESSAGE_ROLE_TOOL:
		return Message{
			Role:       "tool",
			ToolCallID: toolResultCallID(m.Content),
			Content:    contentToString(m.Content),
		}, nil
	}
	return Message{Role: string(m.Role), Content: contentToString(m.Content)}, nil
}

// isEffectivelyEmpty reports whether all strings are empty or whitespace only.
func isEffectivelyEmpty(texts []string) bool {
	for _, t := range texts {
		if strings.TrimSpace(t) != "" {
			return false
		}
	}
	return true
}

// contentToString joins all core.TextPart and recursively processes core.ToolResultPart.
func contentToString(parts []core.ContentParter) string {
	var texts []string
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			texts = append(texts, p.Text)
		case core.ToolResultPart:
			if s := contentToString(p.Content); s != "" {
				texts = append(texts, s)
			}
		}
	}
	return joinTexts(texts)
}

// contentToKimi converts content parts to Kimi multimodal format.
// For a single text part it returns a string; otherwise it returns []ContentParter.
func contentToKimi(parts []core.ContentParter) (any, error) {
	if len(parts) == 1 {
		if p, ok := parts[0].(core.TextPart); ok {
			return p.Text, nil
		}
	}
	var out []ContentParter
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, ContentParter{Type: "text", Text: p.Text})
		case core.ImagePart:
			out = append(out, ContentParter{
				Type: "image_url",
				ImageURL: &struct {
					URL    string `json:"url"`
					Detail string `json:"detail,omitempty"`
				}{
					URL:    p.URL,
					Detail: p.Detail,
				},
			})
		default:
			return nil, fmt.Errorf("kimi: unsupported content part in user message: %T", part)
		}
	}
	return out, nil
}

// toolResultCallID extracts the ToolCallID from the first core.ToolResultPart.
func toolResultCallID(parts []core.ContentParter) string {
	for _, part := range parts {
		if p, ok := part.(core.ToolResultPart); ok {
			return p.ToolCallID
		}
	}
	return ""
}

// joinTexts joins strings with newlines.
func joinTexts(texts []string) string {
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

// toKimiTool converts a core.ToolDefinition to Kimi Tool format.
func toKimiTool(t core.ToolDefinition) (Tool, error) {
	if strings.HasPrefix(t.Name, "$") {
		return Tool{
			Type:     "builtin_function",
			Function: Function{Name: t.Name},
		}, nil
	}
	var params any
	if t.Parameters != nil {
		data, err := json.Marshal(t.Parameters)
		if err != nil {
			return Tool{}, fmt.Errorf("kimi: failed to marshal tool parameters: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return Tool{}, fmt.Errorf("kimi: failed to unmarshal tool parameters: %w", err)
		}
		params = ensurePropertyTypes(m)
	}
	return Tool{
		Type: "function",
		Function: Function{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}, nil
}

// toKimiToolChoice converts a core.ToolChoice to Kimi format.
func toKimiToolChoice(tc core.ToolChoice) any {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return "auto"
	case core.ToolChoiceModeRequired:
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tc.Name,
			},
		}
	case core.ToolChoiceModeNone:
		return "none"
	}
	return "auto"
}

// ensurePropertyTypes recursively traverses a JSON Schema map and ensures every
// property in "properties" has a "type" field, defaulting to "string" when missing.
func ensurePropertyTypes(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}

	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}

	if props, ok := out["properties"].(map[string]any); ok {
		newProps := make(map[string]any, len(props))
		for name, prop := range props {
			propMap, ok := prop.(map[string]any)
			if !ok {
				newProps[name] = prop
				continue
			}
			newProp := make(map[string]any, len(propMap))
			for pk, pv := range propMap {
				newProp[pk] = pv
			}
			if _, hasType := newProp["type"]; !hasType {
				newProp["type"] = "string"
			}
			newProps[name] = ensurePropertyTypes(newProp)
		}
		out["properties"] = newProps
	}

	if items, ok := out["items"]; ok {
		out["items"] = ensurePropertyTypes(items)
	}

	return out
}

// thinkingToReasoningEffort maps thinking configuration to reasoning effort.
func thinkingToReasoningEffort(t string) string {
	switch t {
	case "enabled":
		return "high"
	case "disabled":
		return ""
	}
	return ""
}
