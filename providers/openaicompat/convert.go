package openaicompat

import (
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/types"
)

// ToOpenAIMessages converts core.Message slice to OpenAI wire format.
func ToOpenAIMessages(msgs []core.Message, systemPrompt string) ([]Message, error) {
	var out []Message
	if systemPrompt != "" {
		out = append(out, Message{Role: "system", Content: systemPrompt})
	}
	for i, m := range msgs {
		// RoleTool 消息中包含多个 ToolResultPart 时，拆分成多条 tool 消息
		if m.Role == core.MESSAGE_ROLE_TOOL {
			for _, part := range m.Content {
				if tr, ok := part.(core.ToolResultPart); ok {
					out = append(out, Message{
						Role:       "tool",
						ToolCallID: tr.ToolCallID,
						Content:    contentToString(tr.Content),
					})
				}
			}
			continue
		}
		om, err := toOpenAIMessage(m)
		if err != nil {
			fmt.Printf("[ToOpenAIMessages] msg[%d] role=%s ERROR: %v\n", i, m.Role, err)
			return nil, err
		}
		fmt.Printf("[ToOpenAIMessages] msg[%d] role=%s tool_call_id=%s content_type=%T content=%v\n", i, om.Role, om.ToolCallID, om.Content, om.Content)
		out = append(out, om)
	}
	return out, nil
}

func toOpenAIMessage(m core.Message) (Message, error) {
	switch m.Role {
	case core.MESSAGE_ROLE_SYSTEM:
		return Message{Role: "system", Content: contentToString(m.Content)}, nil
	case core.MESSAGE_ROLE_USER:
		content, err := contentToOpenAI(m.Content)
		if err != nil {
			return Message{}, err
		}
		return Message{Role: "user", Content: content}, nil
	case core.MESSAGE_ROLE_ASSISTANT:
		msg := Message{Role: "assistant"}
		var textParts []string
		for _, part := range m.Content {
			switch p := part.(type) {
			case core.TextPart:
				textParts = append(textParts, p.Text)
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
				return Message{}, fmt.Errorf("openai: unsupported content part in assistant message: %T", part)
			}
		}
		if len(textParts) > 0 {
			msg.Content = joinTexts(textParts)
		}
		return msg, nil
	case core.MESSAGE_ROLE_TOOL:
		if len(m.Content) > 0 {
			return Message{
				Role:       "tool",
				ToolCallID: toolResultCallID(m.Content),
				Content:    contentToString(m.Content),
			}, nil
		}
	}
	return Message{Role: string(m.Role), Content: contentToString(m.Content)}, nil
}

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

func contentToOpenAI(parts []core.ContentParter) (any, error) {
	if len(parts) == 1 {
		switch p := parts[0].(type) {
		case core.TextPart:
			return p.Text, nil
		case core.ToolResultPart:
			return contentToString(p.Content), nil
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
		case core.ToolResultPart:
			out = append(out, ContentParter{Type: "text", Text: contentToString(p.Content)})
		default:
			return nil, fmt.Errorf("openai: unsupported content part in user message: %T", part)
		}
	}
	return out, nil
}

func toolResultCallID(parts []core.ContentParter) string {
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

// ToOpenAITools converts core tool definitions to OpenAI format.
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

// ToCoreResponse converts OpenAI ChatCompletionResponse to core.Response.
func ToCoreResponse(resp *ChatCompletionResponse, model string) (*core.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	choice := resp.Choices[0]
	msg := core.Message{Role: core.MESSAGE_ROLE_ASSISTANT}

	if text, ok := choice.Message.Content.(string); ok && text != "" {
		msg.Content = append(msg.Content, core.TextPart{Text: text})
	}
	if choice.Message.ReasoningContent != "" {
		msg.Content = append(msg.Content, core.ReasoningPart{Text: choice.Message.ReasoningContent})
		if text, ok := choice.Message.Content.(string); !ok || text == "" {
			msg.Content = append(msg.Content, core.TextPart{Text: choice.Message.ReasoningContent})
		}
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
