package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestExtractProviderOptions_Nil(t *testing.T) {
	result := extractProviderOptions(nil)
	if result.Thinking != nil || result.PromptCacheKey != "" {
		t.Error("expected empty options for nil")
	}
}

func TestExtractProviderOptions_KimiType(t *testing.T) {
	input := core.ProviderOptions{"kimi": ProviderOptions{PromptCacheKey: "key-1"}}
	result := extractProviderOptions(input)
	if result.PromptCacheKey != "key-1" {
		t.Errorf("expected key-1, got %s", result.PromptCacheKey)
	}
}

func TestExtractProviderOptions_OtherType(t *testing.T) {
	result := extractProviderOptions(core.ProviderOptions{})
	if result.PromptCacheKey != "" {
		t.Error("expected empty options for non-kimi type")
	}
}
