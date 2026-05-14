package compression

// CompressionConfig controls context compression behavior.
// When the conversation history exceeds Threshold * model context length,
// the Engine summarizes middle messages via the auxiliary provider.
type CompressionConfig struct {
	Enabled     bool    `yaml:"enabled"`      // default true
	Threshold   float64 `yaml:"threshold"`    // default 0.5 (50% of context)
	TargetRatio float64 `yaml:"target_ratio"` // default 0.2 (compress to 20%)
	ProtectLast int     `yaml:"protect_last"` // default 20 messages
	MaxPasses   int     `yaml:"max_passes"`   // default 3
	// PerMessageMaxTokens is the per-message ceiling above which a single
	// message in head/tail is replaced with an aux-LLM summary even when the
	// surrounding messages are compact. Defends against single 200KB+ pastes
	// landing in the protected tail. Default 8000 if 0; set negative to disable.
	PerMessageMaxTokens int `yaml:"per_message_max_tokens,omitempty"`
}
