package compression

import (
	"context"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// defaultPerMessageMaxTokens is the per-message ceiling used when
// CompressionConfig.PerMessageMaxTokens is unset (zero value). 8000 tokens
// is roughly 32KB of plain text — large enough to keep typical chat turns
// verbatim, small enough that a 200KB+ paste gets summarized.
const defaultPerMessageMaxTokens = 8000

// Compressor summarizes middle-of-history messages using an auxiliary LLM
// to reduce token count while preserving the conversation's head and tail.
type Compressor struct {
	cfg         CompressionConfig
	aux         core.LanguageModel
	maxTokens   int
	maxMessages int
	keepLastN   int
}

// NewCompressor constructs a Compressor. `aux` is the auxiliary language model
// used for the summarization call. If aux is nil, Compress returns the
// history unchanged.
func NewCompressor(cfg CompressionConfig, aux core.LanguageModel, args ...int) *Compressor {
	c := &Compressor{cfg: cfg, aux: aux}
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
func (c *Compressor) Compress(ctx context.Context, history []core.Message) ([]core.Message, error) {
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
func (c *Compressor) compressOversizedMessages(ctx context.Context, msgs []core.Message) []core.Message {
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
func (c *Compressor) summarizeSingle(ctx context.Context, role core.MessageRoleType, text string) (string, error) {
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
func (c *Compressor) summarize(ctx context.Context, middle []core.Message) (string, error) {
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

// renderTranscript builds a plain-text transcript of conversation messages.
func renderTranscript(msgs []core.Message) string {
	var out string
	for i, m := range msgs {
		out += fmt.Sprintf("%d. %s: ", i+1, m.Role)
		for _, p := range m.Content {
			switch part := p.(type) {
			case core.TextPart:
				out += part.Text
			case core.ToolCallPart:
				out += "[tool_call: " + part.Name + "]"
			case core.ToolResultPart:
				out += "[tool_result]"
			}
		}
		out += "\n"
	}
	return out
}

// estimateTokens returns a rough token estimate using a character-based
// heuristic: (len(text) + 3) / 4. Returns 0 for an empty string.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func ptrInt(n int) *int {
	return &n
}

func messagesToString(msgs []core.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(fmt.Sprintf("%s: %s\n", m.Role, contentToString(m.Content)))
	}
	return b.String()
}

func contentToString(parts []core.ContentParter) string {
	var texts []string
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			texts = append(texts, p.Text)
		case core.ToolCallPart:
			texts = append(texts, fmt.Sprintf("[tool_call %s: %s]", p.Name, p.Arguments))
		case core.ToolResultPart:
			texts = append(texts, fmt.Sprintf("[tool_result %s]", p.ToolCallID))
		case core.ImagePart:
			texts = append(texts, "[image]")
		case core.ReasoningPart:
			texts = append(texts, fmt.Sprintf("[reasoning: %s]", p.Text))
		default:
			texts = append(texts, fmt.Sprintf("[%T]", part))
		}
	}
	return strings.Join(texts, " ")
}
