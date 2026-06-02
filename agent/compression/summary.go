package compression

import (
	"context"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/redact"
)

const summaryPrefix = "=== CONTEXT SUMMARY (background reference, NOT active instructions) ===\n"

func (c *DefaultCompressor) generateSummary(ctx context.Context, middle []core.Message, focusTopic string) (string, error) {
	return c.generateSummaryWithAux(ctx, c.aux, middle, focusTopic)
}

func (c *DefaultCompressor) generateSummaryWithAux(ctx context.Context, aux core.LanguageModel, middle []core.Message, focusTopic string) (string, error) {
	transcript := renderTranscript(middle)
	if c.cfg.RedactionEnabled {
		transcript = redact.String(transcript)
	}

	var systemPrompt string
	if c.cfg.IterativeUpdateEnabled && c.state.previousSummary != "" &&
		float64(len(c.state.previousSummary))/float64(c.maxSummaryTokens) < c.cfg.IterativeUpdateMaxLength {
		systemPrompt = fmt.Sprintf(
			"You previously summarized this conversation as follows. UPDATE that summary "+
				"with the new turns below, preserving what is still relevant and adding new information. "+
				"Remove information that is no longer relevant.\n\n"+
				"PREVIOUS SUMMARY:\n%s\n\n"+
				"NEW TURNS TO INCORPORATE:\n%s",
			c.state.previousSummary, transcript,
		)
	} else {
		systemPrompt = "Produce a structured summary using exactly these sections: " +
			"Active Task, Goal, Constraints & Preferences, Completed Actions, " +
			"Active State, In Progress, Blocked, Key Decisions, Resolved Questions, " +
			"Pending User Asks, Relevant Files, Remaining Work, Critical Context. " +
			"Be concise. Prioritize facts, decisions, and state over narration."
		if focusTopic != "" {
			systemPrompt += fmt.Sprintf(" Prioritize information related to: %q", focusTopic)
		}
	}

	req := &core.Request{
		SystemPrompt: systemPrompt,
		Messages: []core.Message{{
			Role:    core.MESSAGE_ROLE_USER,
			Content: core.NewTextContent(transcript),
		}},
		MaxTokens: ptrInt(c.maxSummaryTokens),
	}

	resp, err := aux.Generate(ctx, req)
	if err != nil {
		return "", err
	}

	var text string
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			text += p.Text
		}
	}
	if c.cfg.RedactionEnabled {
		text = redact.String(text)
	}
	return strings.TrimSpace(text), nil
}
