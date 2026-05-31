package compression

import "github.com/odysseythink/pantheon/core"

func (c *DefaultCompressor) assemble(head, tail []core.Message, summary string) []core.Message {
	// Add compaction note to system prompt in head
	for i := range head {
		if head[i].Role == core.MESSAGE_ROLE_SYSTEM {
			head[i].Content = append(head[i].Content, core.TextPart{
				Text: "[Context has been compressed. A summary of earlier turns follows.]",
			})
		}
	}

	summaryMsg := core.Message{
		Role:    core.MESSAGE_ROLE_ASSISTANT,
		Content: core.NewTextContent("[Compressed summary of earlier conversation]\n" + summary),
	}

	// Avoid consecutive same-role messages
	if len(tail) > 0 && tail[0].Role == core.MESSAGE_ROLE_ASSISTANT {
		tail[0].Content = append(summaryMsg.Content, tail[0].Content...)
		result := make([]core.Message, 0, len(head)+len(tail))
		result = append(result, head...)
		result = append(result, tail...)
		return result
	}

	result := make([]core.Message, 0, len(head)+1+len(tail))
	result = append(result, head...)
	result = append(result, summaryMsg)
	result = append(result, tail...)
	return result
}
