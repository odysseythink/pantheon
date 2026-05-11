package anthropic

import (
	"context"

	"github.com/odysseythink/ai/core"
)

// Messages sends a non-streaming messages request to the Anthropic API.
func (c *Client) Messages(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	messages, err := ToAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	anthropicReq := MessagesRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: 4096,
		Stream:    false,
	}
	if req.MaxTokens != nil {
		anthropicReq.MaxTokens = *req.MaxTokens
	}
	anthropicReq.Temperature = req.Temperature
	anthropicReq.TopP = req.TopP
	anthropicReq.StopSequences = req.StopSequences
	if len(req.Tools) > 0 {
		anthropicReq.Tools = ToAnthropicTools(req.Tools)
		anthropicReq.ToolChoice = ToAnthropicToolChoice(req.ToolChoice)
	}
	if req.SystemPrompt != "" {
		anthropicReq.System = req.SystemPrompt
	}

	if opts, ok := req.ProviderOptions.Get("anthropic"); ok {
		if ao, ok := opts.(*ProviderOptions); ok {
			if ao.Thinking != nil {
				anthropicReq.Thinking = &ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: ao.Thinking.BudgetTokens,
				}
			}
		}
	}

	var resp MessagesResponse
	if err := c.doJSON(ctx, "POST", "/v1/messages", anthropicReq, &resp); err != nil {
		return nil, err
	}
	return ToCoreResponse(&resp, model)
}

// ToAnthropicToolChoice converts a core.ToolChoice to Anthropic format.
func ToAnthropicToolChoice(tc core.ToolChoice) *ToolChoice {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return &ToolChoice{Type: "auto"}
	case core.ToolChoiceModeNone:
		return &ToolChoice{Type: "auto"}
	case core.ToolChoiceModeRequired:
		if tc.Name != "" {
			return &ToolChoice{Type: "tool", Name: tc.Name}
		}
		return &ToolChoice{Type: "any"}
	}
	return &ToolChoice{Type: "auto"}
}
