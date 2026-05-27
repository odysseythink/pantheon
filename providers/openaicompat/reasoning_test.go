package openaicompat

import (
	"encoding/json"
	"testing"
)

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		modelID string
		want    bool
	}{
		{"o1", true},
		{"o1-preview", true},
		{"o1-mini", true},
		{"my-o1-model", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4", true},
		{"o4-mini", true},
		{"oss", true},
		{"oss-model", true},
		{"gpt-5", true},
		{"gpt-5-chat", true},
		{"gpt-5-mini", true},
		{"gpt-4", false},
		{"gpt-4o", false},
		{"claude-3-5-sonnet", false},
		{"deepseek-chat", false},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := isReasoningModel(tt.modelID)
			if got != tt.want {
				t.Errorf("isReasoningModel(%q) = %v, want %v", tt.modelID, got, tt.want)
			}
		})
	}
}

func TestAdaptRequestForReasoning_RemovesUnsupportedParams(t *testing.T) {
	temp := 0.7
	topP := 0.9
	maxTokens := 100
	freqPenalty := 0.5
	presPenalty := 0.3

	req := ChatCompletionRequest{
		Model:            "o3-mini",
		Temperature:      &temp,
		TopP:             &topP,
		MaxTokens:        &maxTokens,
		FrequencyPenalty: &freqPenalty,
		PresencePenalty:  &presPenalty,
	}

	adaptRequestForReasoning(&req, "o3-mini")

	if req.Temperature != nil {
		t.Errorf("expected temperature to be nil, got %v", *req.Temperature)
	}
	if req.TopP != nil {
		t.Errorf("expected top_p to be nil, got %v", *req.TopP)
	}
	if req.FrequencyPenalty != nil {
		t.Errorf("expected frequency_penalty to be nil, got %v", *req.FrequencyPenalty)
	}
	if req.PresencePenalty != nil {
		t.Errorf("expected presence_penalty to be nil, got %v", *req.PresencePenalty)
	}
	if req.MaxTokens != nil {
		t.Errorf("expected max_tokens to be nil, got %v", *req.MaxTokens)
	}
	if req.MaxCompletionTokens == nil {
		t.Fatal("expected max_completion_tokens to be set")
	}
	if *req.MaxCompletionTokens != 100 {
		t.Errorf("expected max_completion_tokens=100, got %d", *req.MaxCompletionTokens)
	}
}

func TestAdaptRequestForReasoning_NonReasoningModelUnchanged(t *testing.T) {
	temp := 0.7
	maxTokens := 100

	req := ChatCompletionRequest{
		Model:       "gpt-4",
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	}

	adaptRequestForReasoning(&req, "gpt-4")

	if req.Temperature == nil {
		t.Error("expected temperature to remain set for non-reasoning model")
	}
	if req.MaxTokens == nil {
		t.Error("expected max_tokens to remain set for non-reasoning model")
	}
	if req.MaxCompletionTokens != nil {
		t.Error("expected max_completion_tokens to remain unset for non-reasoning model")
	}
}

func TestAdaptRequestForReasoning_JsonRoundTrip(t *testing.T) {
	maxTokens := 200
	req := ChatCompletionRequest{
		Model:     "o1",
		MaxTokens: &maxTokens,
	}
	adaptRequestForReasoning(&req, "o1")

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if _, ok := raw["max_tokens"]; ok {
		t.Error("expected max_tokens to be absent from JSON")
	}
	if _, ok := raw["max_completion_tokens"]; !ok {
		t.Error("expected max_completion_tokens to be present in JSON")
	}
	if _, ok := raw["temperature"]; ok {
		t.Error("expected temperature to be absent from JSON")
	}
	if _, ok := raw["top_p"]; ok {
		t.Error("expected top_p to be absent from JSON")
	}
}
