package skills

import (
	"strings"
	"testing"
)

func catSkills(specs ...[2]string) []*Skill {
	out := make([]*Skill, 0, len(specs))
	for _, sp := range specs {
		out = append(out, &Skill{Name: sp[0], Description: sp[1], Type: SkillTypeInline})
	}
	return out
}

func TestApplyCatalogBudgetUnderBudgetIsUntouched(t *testing.T) {
	skills := catSkills([2]string{"a", "short"}, [2]string{"b", "also short"})
	out, rep := ApplyCatalogBudget(skills, DefaultCatalogBudgetChars)
	if rep.TruncatedDescriptionCount != 0 || rep.OmittedCount != 0 || rep.Warning != "" {
		t.Errorf("report = %+v, want clean", rep)
	}
	if out[0].Description != "short" || out[1].Description != "also short" {
		t.Errorf("descriptions mutated: %q %q", out[0].Description, out[1].Description)
	}
	if rep.IncludedCount != 2 || rep.TotalCount != 2 {
		t.Errorf("counts = %+v", rep)
	}
}

func TestApplyCatalogBudgetClampsOversizedDescription(t *testing.T) {
	huge := strings.Repeat("x", MaxCatalogDescriptionChars*2)
	skills := catSkills([2]string{"big", huge})
	out, rep := ApplyCatalogBudget(skills, DefaultCatalogBudgetChars)
	if len(out[0].Description) > MaxCatalogDescriptionChars {
		t.Errorf("description len = %d, want <= %d", len(out[0].Description), MaxCatalogDescriptionChars)
	}
	if !strings.HasSuffix(out[0].Description, "...") {
		t.Error("truncated description must end with ellipsis")
	}
	if rep.TruncatedDescriptionCount != 1 {
		t.Errorf("TruncatedDescriptionCount = %d, want 1", rep.TruncatedDescriptionCount)
	}
	// The caller's skill must not be mutated.
	if len(skills[0].Description) != len(huge) {
		t.Error("input skill was mutated")
	}
}

func TestApplyCatalogBudgetShrinksDescriptionsToFit(t *testing.T) {
	var specs [][2]string
	for i := 0; i < 20; i++ {
		specs = append(specs, [2]string{strings.Repeat("n", 10), strings.Repeat("d", 200)})
	}
	out, rep := ApplyCatalogBudget(catSkills(specs...), 1000)
	total := 0
	for _, s := range out {
		total += len(s.Name) + len(s.Description)
	}
	if total > 1000 {
		t.Errorf("total catalog size %d exceeds budget 1000", total)
	}
	if rep.TruncatedDescriptionCount == 0 {
		t.Error("expected some descriptions to be truncated")
	}
	if rep.Warning == "" {
		t.Error("expected a truncation warning")
	}
}

func TestApplyCatalogBudgetDropsDescriptionsAndOmitsWhenDesperate(t *testing.T) {
	var specs [][2]string
	for i := 0; i < 50; i++ {
		specs = append(specs, [2]string{strings.Repeat("n", 40), strings.Repeat("d", 400)})
	}
	out, rep := ApplyCatalogBudget(catSkills(specs...), 500)
	for _, s := range out {
		if s.Description != "" {
			t.Errorf("desperate budget must empty descriptions, got %q", s.Description)
		}
	}
	if rep.OmittedCount == 0 {
		t.Error("expected some skills to be omitted")
	}
	if rep.IncludedCount+rep.OmittedCount != rep.TotalCount {
		t.Errorf("counts inconsistent: %+v", rep)
	}
	if !strings.Contains(rep.Warning, "removed") {
		t.Errorf("warning should mention removed descriptions: %q", rep.Warning)
	}
}

func TestBudgetForContextWindow(t *testing.T) {
	if got := BudgetForContextWindow(200_000); got != 16_000 {
		t.Errorf("BudgetForContextWindow(200k) = %d, want 16000 (2%% at 4 chars/token)", got)
	}
	if got := BudgetForContextWindow(0); got != DefaultCatalogBudgetChars {
		t.Errorf("BudgetForContextWindow(0) = %d, want default %d", got, DefaultCatalogBudgetChars)
	}
	if got := BudgetForContextWindow(-5); got != DefaultCatalogBudgetChars {
		t.Errorf("BudgetForContextWindow(-5) = %d, want default %d", got, DefaultCatalogBudgetChars)
	}
}
