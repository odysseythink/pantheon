package compression

import (
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/redact"
)

// renderTranscript builds a plain-text transcript of conversation messages.
func renderTranscript(msgs []core.Message) string {
	var out string
	for i, m := range msgs {
		out += fmt.Sprintf("%d. %s: ", i+1, m.Role)
		for _, p := range m.Content {
			switch part := p.(type) {
			case core.TextPart:
				out += part.Text
			case core.ToolCallPart:
				out += "[tool_call: " + part.Name + "]"
			case core.ToolResultPart:
				out += "[tool_result]"
			case core.ToolResultErrorPart:
				out += "[tool_result_error: " + part.Error + "]"
			}
		}
		out += "\n"
	}
	return out
}

// estimateTokens returns a rough token estimate using a character-based
// heuristic: (len(text) + 3) / 4. Returns 0 for an empty string.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// estimateMessageTokens estimates tokens for an entire message.
func estimateMessageTokens(m core.Message) int {
	total := 0
	for _, p := range m.Content {
		switch part := p.(type) {
		case core.TextPart:
			total += estimateTokens(part.Text)
		case core.ToolCallPart:
			total += estimateTokens(part.Name + part.Arguments)
		case core.ToolResultPart:
			for _, cp := range part.Content {
				if tp, ok := cp.(core.TextPart); ok {
					total += estimateTokens(tp.Text)
				}
			}
		case core.ToolResultErrorPart:
			total += estimateTokens(part.Error)
		case core.ImagePart:
			total += 256 // rough estimate for image
		}
	}
	return total
}

func ptrInt(n int) *int {
	return &n
}

func messagesToString(msgs []core.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(fmt.Sprintf("%s: %s\n", m.Role, contentToString(m.Content)))
	}
	return b.String()
}

func contentToString(parts []core.ContentParter) string {
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
		case core.ToolResultErrorPart:
			texts = append(texts, fmt.Sprintf("[tool_result_error: %s]", p.Error))
		default:
			texts = append(texts, fmt.Sprintf("[%T]", part))
		}
	}
	return strings.Join(texts, " ")
}


func (c *DefaultCompressor) applyRedaction(text string) string {
	if !c.cfg.RedactionEnabled {
		return text
	}
	if len(c.cfg.RedactPatterns) > 0 {
		for _, re := range c.cfg.RedactPatterns {
			text = re.ReplaceAllString(text, "[REDACTED]")
		}
		return text
	}
	return redact.String(text)
}
