package skills

import (
	"fmt"
	"strings"
)

// Catalog budget constants, mirroring the ody skill renderer. The catalog
// that goes into a system prompt is bounded so installing many skills
// cannot silently eat the context window.
const (
	// DefaultCatalogBudgetChars is the fallback budget when the model's
	// context window is unknown.
	DefaultCatalogBudgetChars = 8_000
	// MaxCatalogDescriptionChars caps any single description in the catalog.
	MaxCatalogDescriptionChars = 1_024
	// catalogContextWindowPercent is the share of the context window the
	// catalog may occupy when the window size is known.
	catalogContextWindowPercent = 2
	// approxCharsPerToken converts token budgets to character budgets.
	approxCharsPerToken = 4
	// perItemOverhead accounts for list markup per catalog entry.
	perItemOverhead = 16

	// TruncatedDescriptionSuffix marks a shortened description.
	TruncatedDescriptionSuffix = "..."
)

// CatalogReport summarizes what ApplyCatalogBudget did, for surfacing to
// the user (and eventually to telemetry).
type CatalogReport struct {
	TotalCount                int
	IncludedCount             int
	OmittedCount              int
	TruncatedDescriptionCount int
	Warning                   string
}

// BudgetForContextWindow derives the catalog character budget from the
// model's context window in tokens (2% of the window, at ~4 chars per
// token). Non-positive windows fall back to DefaultCatalogBudgetChars.
func BudgetForContextWindow(windowTokens int64) int {
	if windowTokens <= 0 {
		return DefaultCatalogBudgetChars
	}
	return int(windowTokens) * catalogContextWindowPercent / 100 * approxCharsPerToken
}

// ApplyCatalogBudget returns copies of skills whose descriptions were
// shortened (or dropped) so the rendered catalog fits budgetChars. Inputs
// are never mutated. When even name-only entries exceed the budget, skills
// are omitted from the end of the list. The report describes every
// intervention and carries a user-facing warning when something was lost.
func ApplyCatalogBudget(skills []*Skill, budgetChars int) ([]*Skill, CatalogReport) {
	rep := CatalogReport{TotalCount: len(skills)}
	if budgetChars <= 0 {
		budgetChars = DefaultCatalogBudgetChars
	}

	out := make([]*Skill, 0, len(skills))
	for _, s := range skills {
		cp := *s
		if len(cp.Description) > MaxCatalogDescriptionChars {
			cp.Description = truncateDesc(cp.Description, MaxCatalogDescriptionChars)
			rep.TruncatedDescriptionCount++
		}
		out = append(out, &cp)
	}

	if catalogCost(out) <= budgetChars {
		rep.IncludedCount = len(out)
		return out, rep
	}

	// Phase 1: shrink descriptions with water-filling so the total fits.
	fixed := budgetChars
	for _, s := range out {
		fixed -= len(s.Name) + perItemOverhead
	}
	if fixed < 0 {
		fixed = 0
	}
	applyDescBudget(out, fixed, &rep)

	// Phase 2: still over budget with descriptions empty — omit the tail.
	for len(out) > 0 && catalogCost(out) > budgetChars {
		out = out[:len(out)-1]
		rep.OmittedCount++
	}

	rep.IncludedCount = len(out)
	rep.Warning = buildWarning(out, &rep)
	return out, rep
}

// applyDescBudget distributes descBudget across the skills' descriptions
// with water-filling: every round fixes the descriptions that fit in the
// fair share and truncates the rest, until the budget is consumed. When
// descBudget is 0 every description is emptied.
func applyDescBudget(skills []*Skill, descBudget int, rep *CatalogReport) {
	remaining := make([]*Skill, 0, len(skills))
	for _, s := range skills {
		if len(s.Description) > 0 {
			remaining = append(remaining, s)
		}
	}
	for len(remaining) > 0 {
		share := descBudget / len(remaining)
		next := remaining[:0]
		progress := false
		for _, s := range remaining {
			if len(s.Description) <= share {
				descBudget -= len(s.Description)
				progress = true
			} else {
				next = append(next, s)
			}
		}
		if !progress {
			for _, s := range next {
				s.Description = truncateDesc(s.Description, share)
				rep.TruncatedDescriptionCount++
			}
			return
		}
		remaining = next
	}
}

func catalogCost(skills []*Skill) int {
	total := 0
	for _, s := range skills {
		total += len(s.Name) + len(s.Description) + perItemOverhead
	}
	return total
}

func truncateDesc(desc string, max int) string {
	if len(desc) <= max {
		return desc
	}
	if max <= len(TruncatedDescriptionSuffix) {
		return ""
	}
	return strings.TrimRight(desc[:max-len(TruncatedDescriptionSuffix)], " \n\t") + TruncatedDescriptionSuffix
}

func buildWarning(skills []*Skill, rep *CatalogReport) string {
	var parts []string
	allEmpty := len(skills) > 0
	for _, s := range skills {
		if s.Description != "" {
			allEmpty = false
			break
		}
	}
	switch {
	case allEmpty && rep.TruncatedDescriptionCount > 0:
		parts = append(parts, "Exceeded skills context budget. All skill descriptions were removed.")
	case rep.TruncatedDescriptionCount > 0:
		parts = append(parts, "Skill descriptions were shortened to fit the skills context budget. Disable unused skills to leave more room for the rest.")
	}
	if rep.OmittedCount > 0 {
		parts = append(parts, fmt.Sprintf("%d skills were omitted from the catalog.", rep.OmittedCount))
	}
	return strings.Join(parts, " ")
}
