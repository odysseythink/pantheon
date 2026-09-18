package compression

import "github.com/odysseythink/pantheon/core"

type boundaries struct {
	headEnd   int // exclusive
	tailStart int // inclusive
}

func (c *DefaultCompressor) determineBoundaries(messages []core.Message) boundaries {
	headEnd := c.cfg.ProtectFirstN
	if headEnd > len(messages) {
		headEnd = len(messages)
	}

	tailBudget := c.tailTokenBudget
	tailStart := len(messages)
	tailTokens := 0
	hardMinTail := 3

	for i := len(messages) - 1; i >= headEnd; i-- {
		tokens := estimateMessageTokens(messages[i])
		if tailTokens > 0 && tailTokens+tokens > int(float64(tailBudget)*1.5) {
			break
		}
		tailTokens += tokens
		tailStart = i
		if tailTokens >= tailBudget && len(messages)-tailStart >= hardMinTail {
			break
		}
	}

	// Ensure most recent user message is in tail
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == core.MESSAGE_ROLE_USER {
			if i < tailStart {
				tailStart = i
			}
			break
		}
	}

	// Align to tool pair boundaries
	tailStart = alignToToolPairBoundaries(messages, tailStart)

	if tailStart < headEnd {
		tailStart = headEnd
	}

	return boundaries{headEnd: headEnd, tailStart: tailStart}
}

func alignToToolPairBoundaries(messages []core.Message, tailStart int) int {
	pairs := buildToolPairMap(messages)
	for callIdx, resultIdx := range pairs {
		if callIdx < tailStart && tailStart <= resultIdx {
			tailStart = callIdx
		}
	}
	return tailStart
}

func buildToolPairMap(messages []core.Message) map[int]int {
	calls := make(map[string]int)
	for i, m := range messages {
		for _, p := range m.Content {
			if tc, ok := p.(core.ToolCallPart); ok {
				calls[tc.ID] = i
			}
		}
	}
	pairs := make(map[int]int)
	for i, m := range messages {
		for _, p := range m.Content {
			if tr, ok := p.(core.ToolResultPart); ok {
				if callIdx, exists := calls[tr.ToolCallID]; exists {
					pairs[callIdx] = i
				}
			}
		}
	}
	return pairs
}
