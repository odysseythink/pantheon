package compression

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

const toolOutputSummaryThreshold = 200

func toolResultText(tr core.ToolResultPart) string {
	var texts []string
	for _, p := range tr.Content {
		if tp, ok := p.(core.TextPart); ok {
			texts = append(texts, tp.Text)
		}
	}
	return strings.Join(texts, "")
}

func (c *DefaultCompressor) pruneToolResults(messages []core.Message) []core.Message {
	if !c.cfg.ToolPruningEnabled {
		return messages
	}

	// 1. Deduplicate tool results by content hash
	seenHashes := make(map[string]int)
	for i := range messages {
		for j := range messages[i].Content {
			if tr, ok := messages[i].Content[j].(core.ToolResultPart); ok {
				text := toolResultText(tr)
				hash := fmt.Sprintf("%x", md5.Sum([]byte(text)))
				if lastIdx, exists := seenHashes[hash]; exists && lastIdx != i {
					messages[i].Content[j] = core.TextPart{
						Text: "[Duplicate tool output — same content as a more recent call]",
					}
				} else {
					seenHashes[hash] = i
				}
			}
		}
	}

	// 2. Summarize large tool outputs, strip images, truncate long args
	for i := range messages {
		for j := range messages[i].Content {
			switch part := messages[i].Content[j].(type) {
			case core.ToolResultPart:
				text := toolResultText(part)
				if len(text) > toolOutputSummaryThreshold {
					messages[i].Content[j] = core.TextPart{
						Text: summarizeToolResult(part),
					}
				}
			case core.ImagePart:
				messages[i].Content[j] = core.TextPart{
					Text: "[image: previously shared image]",
				}
			case core.ToolCallPart:
				if len(part.Arguments) > 500 {
					messages[i].Content[j] = core.ToolCallPart{
						ID:        part.ID,
						Name:      part.Name,
						Arguments: truncateJSONArgs(part.Arguments, 500),
					}
				}
			}
		}
	}

	return messages
}

func summarizeToolResult(tr core.ToolResultPart) string {
	text := toolResultText(tr)
	lines := strings.Count(text, "\n")
	return fmt.Sprintf("[tool_result %s: %d chars, %d lines]", tr.ToolCallID, len(text), lines)
}

func truncateJSONArgs(args string, maxLen int) string {
	if len(args) <= maxLen {
		return args
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return args[:maxLen] + "..."
	}
	compact, err := json.Marshal(v)
	if err != nil {
		return args[:maxLen] + "..."
	}
	if len(compact) <= maxLen {
		return string(compact)
	}
	truncated := truncateJSONValues(v, maxLen/2)
	out, _ := json.Marshal(truncated)
	if len(out) > maxLen {
		return string(out[:maxLen]) + "..."
	}
	return string(out)
}

func truncateJSONValues(v any, maxStrLen int) any {
	switch val := v.(type) {
	case map[string]any:
		for k, vv := range val {
			val[k] = truncateJSONValues(vv, maxStrLen)
		}
		return val
	case []any:
		for i, vv := range val {
			val[i] = truncateJSONValues(vv, maxStrLen)
		}
		return val
	case string:
		if len(val) > maxStrLen {
			return val[:maxStrLen] + "..."
		}
		return val
	default:
		return v
	}
}
