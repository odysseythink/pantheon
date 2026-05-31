package compression

import (
	"context"
	"fmt"
	"time"

	"github.com/odysseythink/pantheon/core"
)

// defaultPerMessageMaxTokens is the per-message ceiling used when
// CompressionConfig.PerMessageMaxTokens is unset (zero value). 8000 tokens
// is roughly 32KB of plain text — large enough to keep typical chat turns
// verbatim, small enough that a 200KB+ paste gets summarized.
const defaultPerMessageMaxTokens = 8000

// DefaultCompressor is the built-in ContextEngine implementation.
type DefaultCompressor struct {
	cfg    CompressionConfig
	aux    core.LanguageModel
	state  compressionState

	// Token budgets (recalculated on UpdateModel)
	thresholdTokens  int
	tailTokenBudget  int
	maxSummaryTokens int

	// Model info
	modelName     string
	contextLength int

	// Backward-compat: args from NewCompressor
	maxTokens   int
	maxMessages int
	keepLastN   int
}

// compressionState tracks runtime state for iterative updates & robustness.
type compressionState struct {
	previousSummary           string
	lastCompressionSavingsPct float64
	ineffectiveCount          int
	summaryCooldownUntil      time.Time
	lastSummaryError          error
}

// Compressor is an alias for DefaultCompressor for backward compatibility.
type Compressor = DefaultCompressor

// NewDefaultCompressor constructs a DefaultCompressor. `aux` is the auxiliary language model
// used for the summarization call. If aux is nil, Compress returns the
// history unchanged.
func NewDefaultCompressor(cfg CompressionConfig, aux core.LanguageModel, args ...int) *DefaultCompressor {
	c := &DefaultCompressor{cfg: cfg.WithDefaults(), aux: aux}
	if len(args) > 0 {
		c.maxTokens = args[0]
	}
	if len(args) > 1 {
		c.maxMessages = args[1]
	}
	if len(args) > 2 {
		c.keepLastN = args[2]
	}
	return c
}

// NewCompressor is a backward-compatible alias for NewDefaultCompressor.
func NewCompressor(cfg CompressionConfig, aux core.LanguageModel, args ...int) *Compressor {
	return NewDefaultCompressor(cfg, aux, args...)
}

// Name returns the engine name.
func (c *DefaultCompressor) Name() string { return "default" }

// UpdateFromResponse initializes token budgets from the first usage response.
func (c *DefaultCompressor) UpdateFromResponse(usage core.Usage) error {
	if c.thresholdTokens == 0 && c.contextLength > 0 {
		c.thresholdTokens = int(float64(c.contextLength) * c.cfg.Threshold)
		c.tailTokenBudget = int(float64(c.thresholdTokens) * c.cfg.SummaryTargetRatio)
		c.maxSummaryTokens = min(int(float64(c.contextLength)*0.05), 12000)
	}
	return nil
}

// UpdateModel updates the model info and recalculates token budgets.
func (c *DefaultCompressor) UpdateModel(model string, contextLength int) error {
	c.modelName = model
	c.contextLength = contextLength
	c.thresholdTokens = int(float64(contextLength) * c.cfg.Threshold)
	c.tailTokenBudget = int(float64(c.thresholdTokens) * c.cfg.SummaryTargetRatio)
	c.maxSummaryTokens = min(int(float64(contextLength)*0.05), 12000)
	return nil
}

// GetToolSchemas returns tool schemas exposed by this engine.
func (c *DefaultCompressor) GetToolSchemas() []core.ToolDefinition { return nil }

// HandleToolCall handles a tool call dispatched to this engine.
func (c *DefaultCompressor) HandleToolCall(ctx context.Context, name string, args map[string]any) (string, error) {
	return "", core.ErrNotImplemented
}

// CompressMessages implements ContextEngine.
func (c *DefaultCompressor) CompressMessages(ctx context.Context, messages []core.Message, focusTopic string) ([]core.Message, error) {
	return c.compressInternal(ctx, messages, focusTopic)
}

// Compress summarizes the middle of the history and returns a shortened
// version. The head (first 3 messages) and tail (last ProtectLast messages)
// are preserved by default, but any single text-only message in head/tail
// that exceeds PerMessageMaxTokens is replaced with an aux-LLM summary so a
// 200KB+ paste in the protected tail can't blow the context window on its
// own. The middle is replaced by a single assistant summary message.
//
// If compression is disabled in config, the original is returned.
// If the auxiliary provider is nil, the original is returned.
// If the history is shorter than head + tail + 1, only the per-message
// oversize check runs (the middle-summary step is skipped).
func (c *DefaultCompressor) Compress(ctx context.Context, history []core.Message) ([]core.Message, error) {
	if !c.cfg.Enabled || c.aux == nil {
		return history, nil
	}

	const headCount = 3
	tailCount := c.cfg.ProtectLast
	if tailCount < 1 {
		tailCount = 20
	}

	// History too short for middle compression — but we still want to trim
	// any single oversized message that snuck in.
	if len(history) <= headCount+tailCount {
		return c.compressOversizedMessages(ctx, history), nil
	}

	head := history[:headCount]
	tail := history[len(history)-tailCount:]
	middle := history[headCount : len(history)-tailCount]

	if len(middle) == 0 {
		return c.compressOversizedMessages(ctx, history), nil
	}

	summary, err := c.summarize(ctx, middle)
	if err != nil {
		return nil, fmt.Errorf("compression: summarize: %w", err)
	}

	result := make([]core.Message, 0, headCount+1+tailCount)
	result = append(result, c.compressOversizedMessages(ctx, head)...)
	result = append(result, core.Message{
		Role:    core.MESSAGE_ROLE_ASSISTANT,
		Content: core.NewTextContent("[Compressed summary of earlier conversation]\n" + summary),
	})
	result = append(result, c.compressOversizedMessages(ctx, tail)...)
	return result, nil
}

// compressOversizedMessages replaces each text-only message whose estimated
// token count exceeds the per-message ceiling with an aux-summarized version.
// Tool-use / tool-result blocks pass through untouched because they carry
// structural ids that pair across messages — summarizing them would orphan
// the partner. On summarize-error a message is kept verbatim; the engine
// will surface the eventual provider 400 rather than silently drop content.
func (c *DefaultCompressor) compressOversizedMessages(ctx context.Context, msgs []core.Message) []core.Message {
	threshold := c.cfg.PerMessageMaxTokens
	if threshold == 0 {
		threshold = defaultPerMessageMaxTokens
	}
	if threshold < 0 {
		return msgs
	}
	out := make([]core.Message, 0, len(msgs))
	for _, m := range msgs {
		if !m.IsTextOnly() {
			out = append(out, m)
			continue
		}
		text := m.Text()
		size := estimateTokens(text)
		if size <= threshold {
			out = append(out, m)
			continue
		}
		summary, err := c.summarizeSingle(ctx, m.Role, text)
		if err != nil || summary == "" {
			out = append(out, m)
			continue
		}
		out = append(out, core.Message{
			Role:    m.Role,
			Content: core.NewTextContent("[Summarized large message]\n" + summary),
		})
	}
	return out
}

// summarizeSingle asks the aux provider for a terse summary of a single
// oversized message. The role is passed through to the prompt so the
// summarizer can preserve "what the user said" vs "what the assistant said".
func (c *DefaultCompressor) summarizeSingle(ctx context.Context, role core.MessageRoleType, text string) (string, error) {
	systemPrompt := fmt.Sprintf(
		"You are a summarizer. The %s sent a message that is too large to keep verbatim. "+
			"Produce a terse, structured summary preserving key facts, decisions, code references, "+
			"file paths, error messages, and identifiers. Keep it under 500 words.",
		role,
	)
	req := &core.Request{
		SystemPrompt: systemPrompt,
		Messages: []core.Message{
			{
				Role:    core.MESSAGE_ROLE_USER,
				Content: []core.ContentParter{core.TextPart{Text: text}},
			},
		},
		MaxTokens: ptrInt(1000),
	}
	resp, err := c.aux.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	var out string
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			out += p.Text
		}
	}
	return out, nil
}

// summarize sends the middle messages to the auxiliary provider with
// a terse summarization prompt and returns the assistant's text response.
func (c *DefaultCompressor) summarize(ctx context.Context, middle []core.Message) (string, error) {
	// Build a condensed transcript to hand to the aux provider.
	transcript := renderTranscript(middle)

	systemPrompt := "You are a summarizer. Produce a terse, bullet-point summary of the conversation below, preserving key facts, decisions, and code references. Keep it under 500 words."

	req := &core.Request{
		SystemPrompt: systemPrompt,
		Messages: []core.Message{
			{
				Role:    core.MESSAGE_ROLE_USER,
				Content: []core.ContentParter{core.TextPart{Text: transcript}},
			},
		},
		MaxTokens: ptrInt(1000),
	}

	resp, err := c.aux.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	// Concatenate all text parts from the response.
	var text string
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			text += p.Text
		}
	}
	return text, nil
}

// estimateMessagesTokens sums tokens across all messages.
func estimateMessagesTokens(msgs []core.Message) int {
	total := 0
	for _, m := range msgs {
		total += estimateMessageTokens(m)
	}
	return total
}
// compressInternal is the 5-phase compression orchestrator.
func (c *DefaultCompressor) compressInternal(ctx context.Context, history []core.Message, focusTopic string) ([]core.Message, error) {
	if !c.cfg.Enabled || c.aux == nil {
		return history, nil
	}

	promptTokens := estimateMessagesTokens(history)
	if !c.ShouldCompress(promptTokens) {
		return history, nil
	}

	originalTokens := estimateMessagesTokens(history)

	// Phase 1: Prune tool results
	history = c.pruneToolResults(history)

	// Phase 2: Determine boundaries
	b := c.determineBoundaries(history)
	if b.tailStart <= b.headEnd {
		result := c.compressOversizedMessages(ctx, history)
		c.recordCompressionResult(originalTokens, estimateMessagesTokens(result))
		return result, nil
	}

	head := history[:b.headEnd]
	middle := history[b.headEnd:b.tailStart]
	tail := history[b.tailStart:]

	if len(middle) == 0 {
		result := c.compressOversizedMessages(ctx, history)
		c.recordCompressionResult(originalTokens, estimateMessagesTokens(result))
		return result, nil
	}

	// Phase 3: Generate summary
	summary, err := c.generateSummaryWithFallback(ctx, middle, focusTopic)
	if err != nil {
		return nil, fmt.Errorf("compression: generate summary: %w", err)
	}

	// Phase 4: Assemble
	result := c.assemble(head, tail, summary)

	// Phase 5: Sanitize
	result = c.sanitizeToolPairs(result)

	// Per-message oversize check
	result = c.compressOversizedMessages(ctx, result)

	c.recordCompressionResult(originalTokens, estimateMessagesTokens(result))
	c.state.previousSummary = summary

	return result, nil
}
