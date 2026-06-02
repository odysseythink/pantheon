package compression

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odysseythink/pantheon/core"
)

const (
	ineffectiveThreshold = 0.10
	antiThrashResetRatio = 0.50
	baseCooldown         = 30 * time.Second
	maxCooldown          = 60 * time.Second
)

func (c *DefaultCompressor) isInCooldown() bool {
	if !c.cfg.CooldownEnabled {
		return false
	}
	return time.Now().Before(c.state.summaryCooldownUntil)
}

func (c *DefaultCompressor) enterCooldown(err error) {
	if !c.cfg.CooldownEnabled {
		return
	}
	c.state.lastSummaryError = err

	var cooldown time.Duration
	if c.state.ineffectiveCount >= 5 {
		cooldown = 600 * time.Second
	} else {
		multiplier := time.Duration(min(c.state.ineffectiveCount+1, 3))
		cooldown = c.cfg.CooldownBase + (c.cfg.CooldownBase/2)*multiplier
		if cooldown > c.cfg.CooldownMax {
			cooldown = c.cfg.CooldownMax
		}
	}
	c.state.summaryCooldownUntil = time.Now().Add(cooldown)
}

func (c *DefaultCompressor) recordCompressionResult(originalTokens, compressedTokens int) {
	if originalTokens == 0 {
		return
	}
	savings := float64(originalTokens-compressedTokens) / float64(originalTokens)
	c.state.lastCompressionSavingsPct = savings

	if savings < c.cfg.AntiThrashThreshold {
		c.state.ineffectiveCount++
	} else {
		c.state.ineffectiveCount = 0
	}
}

// ShouldCompress returns true when compression should run, considering
// anti-thrash and cooldown protections.
func (c *DefaultCompressor) ShouldCompress(promptTokens int) bool {
	if !c.cfg.Enabled || c.aux == nil {
		return false
	}
	if c.isInCooldown() {
		return false
	}
	if c.cfg.AntiThrashEnabled && c.state.ineffectiveCount >= c.cfg.AntiThrashMaxConsecutive {
		return false
	}
	if c.thresholdTokens == 0 {
		// Threshold not yet calibrated — allow compression (caller controls via Enabled).
		return true
	}
	return promptTokens > c.thresholdTokens
}

func (c *DefaultCompressor) generateSummaryWithFallback(ctx context.Context, middle []core.Message, focusTopic string) (string, error) {
	c.state.lastFallbackUsed = false

	summary, err := c.generateSummary(ctx, middle, focusTopic)
	if err == nil && summary != "" {
		return summary, nil
	}

	// Level 1: try fallback model
	if c.fallbackAux != nil {
		fallbackSummary, fallbackErr := c.generateSummaryWithAux(ctx, c.fallbackAux, middle, focusTopic)
		if fallbackErr == nil && fallbackSummary != "" {
			c.state.lastFallbackUsed = true
			return fallbackSummary, nil
		}
	}

	// Level 2: static fallback summary
	c.enterCooldown(err)
	c.state.lastFallbackUsed = true
	return c.buildStaticFallbackSummary(middle), nil
}

func (c *DefaultCompressor) buildStaticFallbackSummary(middle []core.Message) string {
	var parts []string
	parts = append(parts, "## Active Task")
	parts = append(parts, extractLastUserMessage(middle))
	parts = append(parts, "## Completed Actions")
	parts = append(parts, extractToolCallList(middle))
	parts = append(parts, "## Note")
	parts = append(parts, "[Summary generation failed. Earlier context may be incomplete.]")
	return strings.Join(parts, "\n\n")
}

func extractLastUserMessage(msgs []core.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == core.MESSAGE_ROLE_USER {
			return msgs[i].Text()
		}
	}
	return "[No user message found]"
}

func extractToolCallList(msgs []core.Message) string {
	var calls []string
	for _, m := range msgs {
		for _, p := range m.Content {
			if tc, ok := p.(core.ToolCallPart); ok {
				calls = append(calls, fmt.Sprintf("- %s", tc.Name))
			}
		}
	}
	if len(calls) == 0 {
		return "[No tool calls]"
	}
	return strings.Join(calls, "\n")
}
