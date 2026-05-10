package openaicompat

import (
	"fmt"

	"github.com/odysseythink/ai/core"
)

func ToOpenAIMessages(msgs []core.Message, systemPrompt string) []Message {
	var out []Message
	if systemPrompt != "" {
		out = append(out, Message{Role: "system", Content: systemPrompt})
	}
	for _, m := range msgs {
		out = append(out, toOpenAIMessage(m))
	}
	return out
}

func toOpenAIMessage(m core.Message) Message {
	switch m.Role {
	case core.RoleSystem:
		return Message{Role: "system", Content: contentToString(m.Content)}
	case core.RoleUser:
		return Message{Role: "user", Content: contentToOpenAI(m.Content)}
	case core.RoleAssistant:
		msg := Message{Role: "assistant"}
		var textParts []string
		for _, part := range m.Content {
			switch p := part.(type) {
			case core.TextPart:
				textParts = append(textParts, p.Text)
			case core.ToolCallPart:
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
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
			}
		}
		if len(textParts) > 0 && len(msg.ToolCalls) == 0 {
			msg.Content = joinTexts(textParts)
		} else if len(textParts) > 0 {
			msg.Content = joinTexts(textParts)
		}
		return msg
	case core.RoleTool:
		if len(m.Content) > 0 {
			return Message{
				Role:       "tool",
				ToolCallID: toolResultCallID(m.Content),
				Content:    contentToString(m.Content),
			}
		}
	}
	return Message{Role: string(m.Role), Content: contentToString(m.Content)}
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		if p, ok := part.(core.TextPart); ok {
			texts = append(texts, p.Text)
		}
	}
	return joinTexts(texts)
}

func contentToOpenAI(parts []core.ContentPart) any {
	if len(parts) == 1 {
		if p, ok := parts[0].(core.TextPart); ok {
			return p.Text
		}
	}
	var out []ContentPart
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, ContentPart{Type: "text", Text: p.Text})
		case core.ImagePart:
			out = append(out, ContentPart{
				Type: "image_url",
				ImageURL: &struct {
					URL    string `json:"url"`
					Detail string `json:"detail,omitempty"`
				}{
					URL:    p.URL,
					Detail: p.Detail,
				},
			})
		}
	}
	return out
}

func toolResultCallID(parts []core.ContentPart) string {
	for _, part := range parts {
		if p, ok := part.(core.ToolResultPart); ok {
			return p.ToolCallID
		}
	}
	return ""
}

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

func ToOpenAITools(tools []core.ToolDefinition) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{
			Type: "function",
			Function: Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

func ToCoreResponse(resp *ChatCompletionResponse, model string) (*core.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	choice := resp.Choices[0]
	msg := core.Message{Role: core.RoleAssistant}

	if text, ok := choice.Message.Content.(string); ok && text != "" {
		msg.Content = append(msg.Content, core.TextPart{Text: text})
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.Content = append(msg.Content, core.ToolCallPart{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	var fr string
	if choice.FinishReason != nil {
		fr = *choice.FinishReason
	}

	var usage core.Usage
	if resp.Usage != nil {
		usage = core.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}

	return &core.Response{
		Message:      msg,
		FinishReason: fr,
		Usage:        usage,
		Model:        model,
	}, nil
}
