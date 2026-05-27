package openaicompat

import "strings"

// isReasoningModel returns true if the given model ID is a reasoning model
// that does not support certain parameters like temperature and top_p.
// The detection rules align with OpenAI's reasoning model families:
//   - o1, o3, o4 series
//   - oss series
//   - gpt-5 series
func isReasoningModel(modelID string) bool {
	return strings.HasPrefix(modelID, "o1") || strings.Contains(modelID, "-o1") ||
		strings.HasPrefix(modelID, "o3") || strings.Contains(modelID, "-o3") ||
		strings.HasPrefix(modelID, "o4") || strings.Contains(modelID, "-o4") ||
		strings.HasPrefix(modelID, "oss") || strings.Contains(modelID, "-oss") ||
		strings.Contains(modelID, "gpt-5") || strings.Contains(modelID, "gpt-5-chat")
}

// adaptRequestForReasoning modifies the chat completion request when a
// reasoning model is used. Reasoning models do not support temperature,
// top_p, frequency_penalty, or presence_penalty, and use
// max_completion_tokens instead of max_tokens.
func adaptRequestForReasoning(req *ChatCompletionRequest, modelID string) {
	if !isReasoningModel(modelID) {
		return
	}
	req.Temperature = nil
	req.TopP = nil
	req.FrequencyPenalty = nil
	req.PresencePenalty = nil
	if req.MaxTokens != nil {
		req.MaxCompletionTokens = req.MaxTokens
		req.MaxTokens = nil
	}
}
