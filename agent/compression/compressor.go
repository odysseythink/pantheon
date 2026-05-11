package compression

import (
	"context"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// Compressor summarizes older messages to keep context within bounds.
type Compressor struct {
	Model       core.LanguageModel
	MaxTokens   int
	MaxMessages int
	KeepLastN   int
}

// Compress returns a reduced message list if thresholds are exceeded.
func (c *Compressor) Compress(ctx context.Context, messages []core.Message) ([]core.Message, error) {
	if c.Model == nil || len(messages) <= c.KeepLastN {
		return messages, nil
	}
	needCompress := false
	if c.MaxMessages > 0 && len(messages) > c.MaxMessages {
		needCompress = true
	}
	if c.MaxTokens > 0 && estimateTokens(messages) > c.MaxTokens {
		needCompress = true
	}
	if !needCompress {
		return messages, nil
	}

	toSummarize := messages[:len(messages)-c.KeepLastN]
	keep := messages[len(messages)-c.KeepLastN:]

	resp, err := c.Model.Generate(ctx, &core.Request{
		Messages: []core.Message{{
			Role: core.RoleUser,
			Content: []core.ContentPart{core.TextPart{Text: fmt.Sprintf(
				"Summarize the following conversation in a few sentences. Be concise:\n\n%s",
				messagesToString(toSummarize),
			)}},
		}},
	})
	if err != nil {
		return nil, err
	}

	summary := core.Message{
		Role:    core.RoleSystem,
		Content: []core.ContentPart{core.TextPart{Text: "Previous context: " + contentToString(resp.Message.Content)}},
	}

	return append([]core.Message{summary}, keep...), nil
}

func estimateTokens(msgs []core.Message) int {
	total := 0
	for _, m := range msgs {
		for _, part := range m.Content {
			if p, ok := part.(core.TextPart); ok {
				total += len(p.Text) / 4
			}
		}
	}
	return total
}

func messagesToString(msgs []core.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(fmt.Sprintf("%s: %s\n", m.Role, contentToString(m.Content)))
	}
	return b.String()
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			texts = append(texts, p.Text)
		case core.ToolCallPart:
			texts = append(texts, fmt.Sprintf("[tool_call %s: %s]", p.Name, p.Arguments))
		case core.ToolResultPart:
			texts = append(texts, fmt.Sprintf("[tool_result %s]", p.ToolCallID))
		case core.ImagePart:
			texts = append(texts, "[image]")
		case core.ReasoningPart:
			texts = append(texts, fmt.Sprintf("[reasoning: %s]", p.Text))
		default:
			texts = append(texts, fmt.Sprintf("[%T]", part))
		}
	}
	return strings.Join(texts, " ")
}
