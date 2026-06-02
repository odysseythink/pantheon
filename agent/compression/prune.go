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

	// Redact secrets in tool results before dedup
	if c.cfg.RedactionEnabled {
		for i := range messages {
			for j := range messages[i].Content {
				if tr, ok := messages[i].Content[j].(core.ToolResultPart); ok {
					messages[i].Content[j] = redactToolResultWithPatterns(c, tr)
				}
			}
		}
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
						Text: c.summarizeToolResult(part),
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

func (c *DefaultCompressor) summarizeToolResult(tr core.ToolResultPart) string {
	text := toolResultText(tr)
	lines := strings.Count(text, "\n")
	if len(text) > 0 && !strings.HasSuffix(text, "\n") {
		lines++
	}

	switch tr.Name {
	case "terminal":
		return fmt.Sprintf("[terminal_output: %d lines, %d chars]", lines, len(text))
	case "browser_navigate":
		url := extractJSONField(text, "url")
		if url == "" {
			url = extractJSONField(text, "title")
		}
		return fmt.Sprintf("[browser_navigate: %s]", url)
	case "create_files":
		count := countJSONArrayItems(text, "created")
		return fmt.Sprintf("[create_files: %d files created]", count)
	case "web_scraping":
		return fmt.Sprintf("[web_scraping: %d chars extracted]", len(text))
	case "session_search":
		count := countJSONArrayItems(text, "results")
		return fmt.Sprintf("[session_search: %d results]", count)
	default:
		return fmt.Sprintf("[tool_result %s: %d chars, %d lines]", tr.ToolCallID, len(text), lines)
	}
}

func extractJSONField(jsonText, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonText), &m); err != nil {
		return ""
	}
	v, ok := m[field].(string)
	if ok {
		return v
	}
	// also try if it's nested under a "result" or similar
	if vm, ok := m[field].(map[string]any); ok {
		if s, ok := vm["url"].(string); ok {
			return s
		}
	}
	return ""
}

func countJSONArrayItems(jsonText, field string) int {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonText), &m); err != nil {
		return 0
	}
	arr, ok := m[field].([]any)
	if ok {
		return len(arr)
	}
	return 0
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
func redactToolResultWithPatterns(c *DefaultCompressor, tr core.ToolResultPart) core.ToolResultPart {
	redacted := make([]core.ContentParter, len(tr.Content))
	for i, p := range tr.Content {
		if tp, ok := p.(core.TextPart); ok {
			redacted[i] = core.TextPart{Text: c.applyRedaction(tp.Text)}
		} else {
			redacted[i] = p
		}
	}
	tr.Content = redacted
	return tr
}
