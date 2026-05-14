package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

func ToAnthropicMessages(msgs []core.Message) ([]Message, error) {
	var out []Message
	for _, m := range msgs {
		if m.Role == core.MESSAGE_ROLE_SYSTEM {
			continue
		}
		content, err := toAnthropicContent(m.Content)
		if err != nil {
			return nil, err
		}
		role := "user"
		if m.Role == core.MESSAGE_ROLE_ASSISTANT {
			role = "assistant"
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return out, nil
}

func toAnthropicContent(parts []core.ContentParter) ([]Content, error) {
	var out []Content
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, Content{Type: "text", Text: p.Text})
		case core.ImagePart:
			source := &ImageSource{Type: "base64", MediaType: p.MIMEType}
			if len(p.Data) > 0 {
				source.Data = base64.StdEncoding.EncodeToString(p.Data)
			} else if p.URL != "" {
				return nil, fmt.Errorf("anthropic: image URLs must be fetched first")
			}
			out = append(out, Content{Type: "image", Source: source})
		case core.ToolCallPart:
			var input map[string]any
			if p.Arguments != "" {
				_ = json.Unmarshal([]byte(p.Arguments), &input)
			}
			out = append(out, Content{Type: "tool_use", ID: p.ID, Name: p.Name, Input: input})
		case core.ToolResultPart:
			resultContent := []Content{{Type: "text", Text: contentToString(p.Content)}}
			out = append(out, Content{Type: "tool_result", ToolUseID: p.ToolCallID, Content: resultContent, IsError: p.IsError})
		default:
			return nil, fmt.Errorf("unsupported content part: %T", part)
		}
	}
	return out, nil
}

func contentToString(parts []core.ContentParter) string {
	var texts []string
	for _, part := range parts {
		if p, ok := part.(core.TextPart); ok {
			texts = append(texts, p.Text)
		}
	}
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

// ToAnthropicTools converts core tool definitions to Anthropic format.
func ToAnthropicTools(tools []core.ToolDefinition) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}
	return out
}

func ToCoreResponse(resp *MessagesResponse, model string) (*core.Response, error) {
	msg := core.Message{Role: core.MESSAGE_ROLE_ASSISTANT}
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			msg.Content = append(msg.Content, core.TextPart{Text: c.Text})
		case "thinking":
			msg.Content = append(msg.Content, core.ReasoningPart{Text: c.Thinking, Signature: c.Signature})
		case "tool_use":
			args, _ := json.Marshal(c.Input)
			msg.Content = append(msg.Content, core.ToolCallPart{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: string(args),
			})
		}
	}

	var fr string
	if resp.StopReason != nil {
		fr = *resp.StopReason
	}

	var usage core.Usage
	if resp.Usage != nil {
		usage = core.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}

	return &core.Response{
		Message:      msg,
		FinishReason: fr,
		Usage:        usage,
		Model:        model,
	}, nil
}
