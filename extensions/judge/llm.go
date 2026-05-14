package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// LLM is a Judge implementation that asks an LLM to score the turn.
type LLM struct {
	model core.LanguageModel
}

// NewLLM constructs an LLM judge using the supplied auxiliary model.
func NewLLM(model core.LanguageModel) *LLM { return &LLM{model: model} }

// Run satisfies Interface.
func (j *LLM) Run(ctx context.Context, in Input) (*Verdict, error) {
	if j.model == nil {
		return &Verdict{Outcome: "unknown"}, nil
	}
	prompt := buildJudgePrompt(in)
	resp, err := j.model.Generate(ctx, &core.Request{
		SystemPrompt: judgeSystemPrompt,
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: prompt}}},
		},
	})
	if err != nil {
		return &Verdict{Outcome: "unknown"}, nil
	}

	raw := strings.TrimSpace(messageText(resp.Message))
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var parsed struct {
		Outcome         string       `json:"outcome"`
		MemoriesUsed    []string     `json:"memories_used"`
		SkillsToExtract []SkillDraft `json:"skills_to_extract"`
		Reasoning       string       `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return &Verdict{Outcome: "unknown"}, nil
	}
	if parsed.Outcome == "" {
		parsed.Outcome = "unknown"
	}
	return &Verdict{
		Outcome:         parsed.Outcome,
		MemoriesUsed:    parsed.MemoriesUsed,
		SkillsToExtract: parsed.SkillsToExtract,
		Reasoning:       parsed.Reasoning,
	}, nil
}

const judgeSystemPrompt = `You are a post-conversation judge. Given the transcript and the memories/skills injected into the system prompt, produce a JSON verdict.

Rules:
- outcome: "success" if the user's request was resolved cleanly; "struggle" if the agent retried, backtracked, or the user had to restate; "failure" if unresolved or wrong.
- memories_used: the subset of injected memory IDs that materially influenced the assistant's reply. Exclude memories the agent clearly ignored.
- skills_to_extract: only populate when outcome != "success". Each skill must be reusable beyond this conversation.

Reply ONLY with JSON, no fences.`

func buildJudgePrompt(in Input) string {
	var b strings.Builder
	b.WriteString("# Transcript\n")
	for _, m := range in.History {
		text := messageText(m)
		if len(text) > 2000 {
			text = text[:2000] + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n", m.Role, text)
	}
	b.WriteString("\n# Injected memories\n")
	for _, m := range in.InjectedMemories {
		fmt.Fprintf(&b, "- id=%s content=%q\n", m.ID, m.Content)
	}
	if len(in.InjectedSkills) > 0 {
		b.WriteString("\n# Injected skills\n")
		for _, s := range in.InjectedSkills {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
	}
	return b.String()
}

// messageText extracts the first text part from a message, or returns
// an empty string if there is no text.
func messageText(m core.Message) string {
	for _, p := range m.Content {
		if tp, ok := p.(core.TextPart); ok {
			return tp.Text
		}
	}
	return ""
}
