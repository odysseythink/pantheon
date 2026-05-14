package kimi

import (
	"errors"

	"github.com/odysseythink/pantheon/core"
)

func parseCompletionResponse(resp *ChatCompletionResponse, model string) (*core.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, errors.New("kimi: no choices in response")
	}
	choice := resp.Choices[0]
	msg := core.Message{Role: core.MESSAGE_ROLE_ASSISTANT}
	if choice.Message.ReasoningContent != "" {
		msg.Content = append(msg.Content, core.ReasoningPart{Text: choice.Message.ReasoningContent})
	}
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
	finishReason := ""
	if choice.FinishReason != nil {
		finishReason = *choice.FinishReason
	}
	usage := parseUsage(resp.Usage)
	return &core.Response{
		Message:      msg,
		FinishReason: finishReason,
		Usage:        usage,
		Model:        model,
	}, nil
}

func parseUsage(u *Usage) core.Usage {
	if u == nil {
		return core.Usage{}
	}
	// TODO: expose cached_tokens via core.Usage once the field is added.
	cached := 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	} else {
		cached = u.CachedTokens
	}
	_ = cached
	return core.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}
