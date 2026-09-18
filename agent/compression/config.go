package compression

import (
	"regexp"
	"time"
)

// CompressionConfig controls context compression behavior.
// When the conversation history exceeds Threshold * model context length,
// the Engine summarizes middle messages via the auxiliary provider.
type CompressionConfig struct {
	Enabled             bool    `yaml:"enabled"`      // default true
	Threshold           float64 `yaml:"threshold"`    // default 0.5 (50% of context)
	TargetRatio         float64 `yaml:"target_ratio"` // default 0.2 (compress to 20%)
	ProtectLast         int     `yaml:"protect_last"` // default 20 messages
	MaxPasses           int     `yaml:"max_passes"`   // default 3
	PerMessageMaxTokens int     `yaml:"per_message_max_tokens,omitempty"`

	Engine                   string           `yaml:"engine"`
	SummaryModel             string           `yaml:"summary_model"`
	FallbackModel            string           `yaml:"fallback_model"`
	ProtectFirstN            int              `yaml:"protect_first_n"`
	SummaryTargetRatio       float64          `yaml:"summary_target_ratio"`
	MaxSummaryTokens         int              `yaml:"max_summary_tokens"`
	AntiThrashEnabled        bool             `yaml:"anti_thrash_enabled"`
	AntiThrashThreshold      float64          `yaml:"anti_thrash_threshold"`
	AntiThrashMaxConsecutive int              `yaml:"anti_thrash_max_consecutive"`
	CooldownEnabled          bool             `yaml:"cooldown_enabled"`
	CooldownBase             time.Duration    `yaml:"cooldown_base"`
	CooldownMax              time.Duration    `yaml:"cooldown_max"`
	RedactionEnabled         bool             `yaml:"redaction_enabled"`
	RedactPatterns           []*regexp.Regexp `yaml:"redact_patterns,omitempty"`
	ToolPruningEnabled       bool             `yaml:"tool_pruning_enabled"`
	IterativeUpdateEnabled   bool             `yaml:"iterative_update_enabled"`
	IterativeUpdateMaxLength float64          `yaml:"iterative_update_max_length"`
}

// WithDefaults returns a copy with zero values filled in.
func (cfg CompressionConfig) WithDefaults() CompressionConfig {
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.5
	}
	if cfg.TargetRatio == 0 {
		cfg.TargetRatio = 0.2
	}
	if cfg.ProtectLast == 0 {
		cfg.ProtectLast = 20
	}
	if cfg.MaxPasses == 0 {
		cfg.MaxPasses = 3
	}
	if cfg.PerMessageMaxTokens == 0 {
		cfg.PerMessageMaxTokens = 8000
	}
	if cfg.Engine == "" {
		cfg.Engine = "default"
	}
	if cfg.ProtectFirstN == 0 {
		cfg.ProtectFirstN = 3
	}
	if cfg.SummaryTargetRatio == 0 {
		cfg.SummaryTargetRatio = cfg.TargetRatio
	}
	if cfg.AntiThrashThreshold == 0 {
		cfg.AntiThrashThreshold = 0.10
	}
	if cfg.AntiThrashMaxConsecutive == 0 {
		cfg.AntiThrashMaxConsecutive = 2
	}
	if cfg.CooldownBase == 0 {
		cfg.CooldownBase = 30 * time.Second
	}
	if cfg.CooldownMax == 0 {
		cfg.CooldownMax = 60 * time.Second
	}
	if cfg.IterativeUpdateMaxLength == 0 {
		cfg.IterativeUpdateMaxLength = 0.80
	}
	return cfg
}

// DefaultCompressionConfig returns a CompressionConfig with all defaults applied.
// All feature flags are enabled by default.
func DefaultCompressionConfig() CompressionConfig {
	return CompressionConfig{
		Enabled:                true,
		AntiThrashEnabled:      true,
		CooldownEnabled:        true,
		RedactionEnabled:       true,
		ToolPruningEnabled:     true,
		IterativeUpdateEnabled: true,
	}.WithDefaults()
}
