// Package skills provides skill extraction from completed conversations.
package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/judge"
)

// Evolver extracts reusable skill snippets from completed conversations
// and persists them as Markdown files in skillDir.
type Evolver struct {
	llm      core.LanguageModel
	skillDir string
	// OnExtracted is called for each skill file successfully written.
	// reason is "verdict" when the skill came from a judge verdict, or
	// "legacy" when it came from the LLM-extraction fallback path.
	OnExtracted func(filename, reason string)
	// OnError is called for non-fatal errors during extraction.
	OnError func(err error)
}

// NewEvolver constructs an Evolver.
// llm may be nil — in that case Extract is a no-op for the legacy path.
// skillDir is the directory where .md skill files are written.
func NewEvolver(llm core.LanguageModel, skillDir string) *Evolver {
	return &Evolver{llm: llm, skillDir: skillDir}
}

// Extract analyses the conversation history and persists skills.
// When verdict is non-nil, directly persists SkillsToExtract from the judge.
// When verdict is nil, falls back to the legacy LLM-extraction path.
// Always ensures skillDir exists.
func (ev *Evolver) Extract(ctx context.Context, turns []core.Message, verdict *judge.Verdict) error {
	if ev == nil {
		return nil
	}

	if err := os.MkdirAll(ev.skillDir, 0o755); err != nil {
		return fmt.Errorf("evolver: mkdir %s: %w", ev.skillDir, err)
	}

	if verdict != nil {
		for _, d := range verdict.SkillsToExtract {
			body := strings.TrimSpace(d.Body)
			if body == "" {
				continue
			}
			slug := makeSlug(body)
			if slug == "skill" {
				slug = makeSlug(body)
			}
			filename := fmt.Sprintf("%s-%s.md", time.Now().UTC().Format("20060102-150405"), slug)
			path := filepath.Join(ev.skillDir, filename)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				return fmt.Errorf("evolver: write %s: %w", path, err)
			}
			if ev.OnExtracted != nil {
				ev.OnExtracted(filepath.Base(path), "verdict")
			}
		}
		return nil
	}

	// Legacy path: no judge, full LLM extraction.
	if ev.llm == nil || len(turns) == 0 {
		return nil
	}

	conversation := formatTurns(turns)
	prompt := fmt.Sprintf(`You are a skill extraction assistant. Review this conversation and determine if it contains a reusable operational pattern, technique, or workflow that would be helpful in future conversations.

If you find a reusable skill, respond with a Markdown snippet in EXACTLY this format:
---
## <title>
**When to use:** <one-sentence trigger condition>

<step-by-step instructions>
---

If there is no reusable skill in this conversation, respond with exactly: NONE

Conversation:
%s`, conversation)

	resp, err := ev.llm.Generate(ctx, &core.Request{
		SystemPrompt: "You extract reusable skill patterns from conversations. Reply only as instructed.",
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: prompt}}},
		},
	})
	if err != nil {
		return nil // extraction is best-effort
	}

	raw := ""
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			raw += p.Text
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "NONE" || raw == "" {
		return nil
	}

	slug := makeSlug(raw)
	filename := fmt.Sprintf("%s-%s.md", time.Now().UTC().Format("20060102-150405"), slug)
	path := filepath.Join(ev.skillDir, filename)
	err = os.WriteFile(path, []byte(raw), 0o644)
	if err == nil && ev.OnExtracted != nil {
		ev.OnExtracted(filepath.Base(path), "legacy")
	}
	return err
}

func formatTurns(turns []core.Message) string {
	var sb strings.Builder
	for _, t := range turns {
		sb.WriteString(string(t.Role))
		sb.WriteString(": ")
		for _, p := range t.Content {
			if tp, ok := p.(core.TextPart); ok {
				sb.WriteString(tp.Text)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func makeSlug(content string) string {
	lines := strings.SplitN(content, "\n", 5)
	title := ""
	for _, l := range lines {
		l = strings.TrimPrefix(l, "## ")
		l = strings.TrimPrefix(l, "# ")
		l = strings.TrimPrefix(l, "---")
		l = strings.TrimSpace(l)
		if l != "" {
			title = l
			break
		}
	}
	if title == "" {
		title = content
	}
	if len(title) > 40 {
		title = title[:40]
	}
	slug := nonAlnum.ReplaceAllString(strings.ToLower(title), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "skill"
	}
	return slug
}
