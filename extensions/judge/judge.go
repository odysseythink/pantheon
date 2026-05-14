// Package judge runs LLM-as-judge over a completed conversation,
// returning a quality verdict and any extractable skill drafts.
package judge

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Verdict summarizes a completed conversation for downstream
// consumers (memory reinforcement, skill extraction, telemetry).
type Verdict struct {
	// Outcome is one of "success", "struggle", "failure", or "unknown".
	Outcome string
	// MemoriesUsed lists the IDs of injected memories that materially
	// influenced the assistant reply, as judged by the aux LLM.
	MemoriesUsed []string
	// SkillsToExtract contains reusable patterns the judge recommends
	// persisting. Populated only when Outcome != "success".
	SkillsToExtract []SkillDraft
	// Reasoning is a terse natural-language note, for telemetry only.
	Reasoning string
}

// SkillDraft is a minimal description of a skill worth saving.
type SkillDraft struct {
	Name        string
	Description string
	Body        string
}

// InjectedMemory is a minimal view of one memory surfaced to the
// agent for a turn. The ID lets the judge name the memory in its
// Verdict.MemoriesUsed output.
type InjectedMemory struct {
	ID      string
	Content string
}

// ActiveSkill is one skill that was injected into the system prompt
// for the conversation under review.
type ActiveSkill struct {
	Name        string
	Description string
	Body        string
}

// Input bundles everything an Interface implementation needs.
type Input struct {
	History          []core.Message
	InjectedMemories []InjectedMemory
	InjectedSkills   []ActiveSkill
	Platform         string
}

// Interface scores a completed conversation. Implementations must be
// safe to leave nil (no-op behavior). Implementations should be
// best-effort — failures must not propagate.
type Interface interface {
	Run(ctx context.Context, in Input) (*Verdict, error)
}
