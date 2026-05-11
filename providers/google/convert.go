package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/ai/core"
)

func toGeminiMessages(msgs []core.Message) ([]Content, error) {
	var out []Content
	for _, m := range msgs {
		role := "user"
		switch m.Role {
		case core.RoleUser:
			role = "user"
		case core.RoleAssistant:
			role = "model"
		case core.RoleTool:
			role = "user"
		}
		parts, err := toGeminiParts(m.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, Content{Role: role, Parts: parts})
	}
	return out, nil
}

func toGeminiParts(parts []core.ContentPart) ([]Part, error) {
	var out []Part
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, Part{Text: p.Text})
		case core.ImagePart:
			blob := &Blob{MimeType: p.MIMEType}
			if len(p.Data) > 0 {
				blob.Data = base64.StdEncoding.EncodeToString(p.Data)
			} else if p.URL != "" {
				return nil, fmt.Errorf("google: image URLs must be fetched first")
			}
			out = append(out, Part{InlineData: blob})
		case core.AudioPart:
			blob := &Blob{MimeType: p.MIMEType}
			if len(p.Data) > 0 {
				blob.Data = base64.StdEncoding.EncodeToString(p.Data)
			} else if p.URL != "" {
				return nil, fmt.Errorf("google: audio URLs must be fetched first")
			}
			out = append(out, Part{InlineData: blob})
		case core.DocumentPart:
			blob := &Blob{MimeType: p.MIMEType}
			if len(p.Data) > 0 {
				blob.Data = base64.StdEncoding.EncodeToString(p.Data)
			}
			out = append(out, Part{InlineData: blob})
		case core.ToolCallPart:
			var args map[string]interface{}
			if p.Arguments != "" {
				_ = json.Unmarshal([]byte(p.Arguments), &args)
			}
			out = append(out, Part{FunctionCall: &FunctionCall{Name: p.Name, Args: args}})
		case core.ToolResultPart:
			out = append(out, Part{FunctionResponse: &FunctionResponse{
				Name:     p.ToolCallID,
				Response: map[string]interface{}{"result": contentToString(p.Content)},
			}})
		default:
			return nil, fmt.Errorf("google: unsupported content part: %T", part)
		}
	}
	return out, nil
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		if p, ok := part.(core.TextPart); ok {
			texts = append(texts, p.Text)
		}
	}
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

func toGeminiTools(tools []core.ToolDefinition) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{
			FunctionDeclarations: []FunctionDeclaration{{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			}},
		})
	}
	return out
}

func toGeminiToolConfig(tc core.ToolChoice) *ToolConfig {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "AUTO"}}
	case core.ToolChoiceModeNone:
		return &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "NONE"}}
	case core.ToolChoiceModeRequired:
		if tc.Name != "" {
			return &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{tc.Name}}}
		}
		return &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "ANY"}}
	}
	return nil
}

func toCoreResponse(resp *GenerateContentResponse, model string) (*core.Response, error) {
	if len(resp.Candidates) == 0 {
		if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
			return nil, &core.ProviderError{
				Message: fmt.Sprintf("prompt blocked: %s", resp.PromptFeedback.BlockReason),
				Code:    resp.PromptFeedback.BlockReason,
			}
		}
		return nil, fmt.Errorf("no candidates in response")
	}

	candidate := resp.Candidates[0]
	msg := core.Message{Role: core.RoleAssistant}

	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			msg.Content = append(msg.Content, core.TextPart{Text: part.Text})
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			msg.Content = append(msg.Content, core.ToolCallPart{
				ID:        part.FunctionCall.Name,
				Name:      part.FunctionCall.Name,
				Arguments: string(args),
			})
		}
	}

	var usage core.Usage
	if resp.UsageMetadata != nil {
		usage = core.Usage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}

	return &core.Response{
		Message:      msg,
		FinishReason: candidate.FinishReason,
		Usage:        usage,
		Model:        model,
	}, nil
}
