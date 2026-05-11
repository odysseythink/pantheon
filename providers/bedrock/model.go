package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/odysseythink/ai/core"
	"github.com/odysseythink/ai/providers/anthropic"
)

type LanguageModel struct {
	provider *Provider
	client   *bedrockClient
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	messages, err := anthropic.ToAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	anthropicReq := anthropic.MessagesRequest{
		Model:         m.model,
		Messages:      messages,
		MaxTokens:     defaultMaxTokens(req.MaxTokens),
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.StopSequences,
	}
	if req.SystemPrompt != "" {
		anthropicReq.System = req.SystemPrompt
	}
	if len(req.Tools) > 0 {
		anthropicReq.Tools = anthropic.ToAnthropicTools(req.Tools)
		anthropicReq.ToolChoice = anthropic.ToAnthropicToolChoice(req.ToolChoice)
	}

	var resp anthropic.MessagesResponse
	if err := m.client.invoke(ctx, m.model, anthropicReq, &resp); err != nil {
		return nil, err
	}
	return anthropic.ToCoreResponse(&resp, m.model)
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	messages, err := anthropic.ToAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	anthropicReq := anthropic.MessagesRequest{
		Model:         m.model,
		Messages:      messages,
		MaxTokens:     defaultMaxTokens(req.MaxTokens),
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		StopSequences: req.StopSequences,
	}
	if req.SystemPrompt != "" {
		anthropicReq.System = req.SystemPrompt
	}
	if len(req.Tools) > 0 {
		anthropicReq.Tools = anthropic.ToAnthropicTools(req.Tools)
		anthropicReq.ToolChoice = anthropic.ToAnthropicToolChoice(req.ToolChoice)
	}

	body, err := m.client.invokeStream(ctx, m.model, anthropicReq)
	if err != nil {
		return nil, err
	}

	return func(yield func(*core.StreamPart, error) bool) {
		defer body.Close()
		decoder := eventstream.NewDecoder()
		var payloadBuf []byte
		for {
			msg, err := decoder.Decode(body, payloadBuf)
			if err != nil {
				if err == io.EOF {
					return
				}
				yield(nil, err)
				return
			}
			payloadBuf = msg.Payload[:0]

			var chunk anthropic.MessagesResponse
			if err := json.Unmarshal(msg.Payload, &chunk); err != nil {
				yield(nil, fmt.Errorf("parse stream chunk: %w", err))
				return
			}

			if len(chunk.Content) > 0 {
				for _, content := range chunk.Content {
					if content.Type == "text" && content.Text != "" {
						sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: content.Text}
						if !yield(sp, nil) {
							return
						}
					}
					if content.Type == "tool_use" {
						args, _ := json.Marshal(content.Input)
						sp := &core.StreamPart{
							Type: core.StreamPartTypeToolCall,
							ToolCall: &core.ToolCallPart{
								ID:        content.ID,
								Name:      content.Name,
								Arguments: string(args),
							},
						}
						if !yield(sp, nil) {
							return
						}
					}
				}
			}
			if chunk.StopReason != nil && *chunk.StopReason != "" {
				sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: *chunk.StopReason}
				if !yield(sp, nil) {
					return
				}
			}
		}
	}, nil
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
		Tools: []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "generate_object"},
	}
	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return extractObjectResponse(resp, m.model)
}

func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("StreamObject not yet implemented")
}

func extractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.ToolCallPart); ok {
			if err := json.Unmarshal([]byte(p.Arguments), &obj); err != nil {
				return nil, fmt.Errorf("parse tool arguments: %w", err)
			}
			break
		}
	}
	if obj == nil {
		return nil, core.ErrNoObjectGenerated
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}

func defaultMaxTokens(n *int) int {
	if n != nil {
		return *n
	}
	return 4096
}
