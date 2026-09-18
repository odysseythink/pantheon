package compression

import "github.com/odysseythink/pantheon/core"

func (c *DefaultCompressor) sanitizeToolPairs(messages []core.Message) []core.Message {
	survivingCalls := make(map[string]bool)
	for _, m := range messages {
		for _, p := range m.Content {
			if tc, ok := p.(core.ToolCallPart); ok {
				survivingCalls[tc.ID] = true
			}
		}
	}

	out := make([]core.Message, 0, len(messages))
	for _, m := range messages {
		filtered := make([]core.ContentParter, 0, len(m.Content))
		for _, p := range m.Content {
			switch part := p.(type) {
			case core.ToolResultPart:
				if !survivingCalls[part.ToolCallID] {
					continue // orphan result
				}
				filtered = append(filtered, part)
			default:
				filtered = append(filtered, p)
			}
		}
		if len(filtered) > 0 {
			m.Content = filtered
			out = append(out, m)
		}
	}

	return injectStubResults(out)
}

func injectStubResults(messages []core.Message) []core.Message {
	needed := make(map[string]bool)
	for _, m := range messages {
		for _, p := range m.Content {
			if tc, ok := p.(core.ToolCallPart); ok {
				needed[tc.ID] = true
			}
		}
	}
	for _, m := range messages {
		for _, p := range m.Content {
			if tr, ok := p.(core.ToolResultPart); ok {
				delete(needed, tr.ToolCallID)
			}
		}
	}

	if len(needed) == 0 {
		return messages
	}

	var stubs []core.ContentParter
	for id := range needed {
		stubs = append(stubs, core.ToolResultPart{
			ToolCallID: id,
			Content:    []core.ContentParter{core.TextPart{Text: "[Tool result was compressed — refer to summary for outcome]"}},
		})
	}

	stubMsg := core.Message{
		Role:    core.MESSAGE_ROLE_TOOL,
		Content: stubs,
	}
	return append(messages, stubMsg)
}
