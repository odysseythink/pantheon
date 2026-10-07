package skills

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSkillFile is a small helper: create dir/SKILL.md with content.
func writeSkillFile(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseSkillTypeAndTriggers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "flow-skill")
	path := writeSkillFile(t, dir, `---
name: release-flow
description: Ship a release end to end.
type: flow
triggers:
  - release
  - ship it
---
Body text.
`)
	if err := os.WriteFile(filepath.Join(dir, "flow.yaml"), []byte("phases:\n  - id: main\n    steps:\n      - agent: worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Type != SkillTypeFlow {
		t.Errorf("Type = %q, want %q", s.Type, SkillTypeFlow)
	}
	if len(s.Triggers) != 2 || s.Triggers[0] != "release" || s.Triggers[1] != "ship it" {
		t.Errorf("Triggers = %v", s.Triggers)
	}
}

func TestParseSkillTypeDefaultIsInline(t *testing.T) {
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "plain"), `---
name: plain
description: no type declared
---
Body.
`)
	s, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Type != SkillTypeInline {
		t.Errorf("Type = %q, want default %q", s.Type, SkillTypeInline)
	}
}

func TestParseSkillTypeRejectsUnknown(t *testing.T) {
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "bad"), `---
name: bad
description: unknown type
type: wizard
---
Body.
`)
	if _, err := ParseSkillFile(path); err == nil {
		t.Fatal("expected error for unknown skill type")
	}
}

func TestParseHiddenInModesBothSpellings(t *testing.T) {
	for _, key := range []string{"hidden_in_modes", "hiddenInModes"} {
		path := writeSkillFile(t, filepath.Join(t.TempDir(), "m"), `---
name: m
description: d
`+key+`:
  - product
  - plan
---
Body.
`)
		s, err := ParseSkillFile(path)
		if err != nil {
			t.Fatalf("%s: parse: %v", key, err)
		}
		if len(s.HiddenInModes) != 2 || s.HiddenInModes[0] != "product" {
			t.Errorf("%s: HiddenInModes = %v", key, s.HiddenInModes)
		}
	}
}

func TestParseDisableModelInvocation(t *testing.T) {
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "d"), `---
name: d
description: d
disable_model_invocation: true
---
Body.
`)
	s, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !s.DisableModelInvocation {
		t.Error("DisableModelInvocation = false, want true")
	}
}

func TestParsePolicyInterfaceDependencies(t *testing.T) {
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "full"), `---
name: full
description: d
policy:
  allow_implicit_invocation: false
  products:
    - odybox
interface:
  display_name: Full Skill
  short_description: short
  brand_color: "#ff0000"
  default_prompt: Do the thing
dependencies:
  skills:
    - base-skill
  tools:
    - type: mcp
      value: filesystem
      description: needs fs access
---
Body.
`)
	s, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Policy.AllowImplicitInvocation == nil || *s.Policy.AllowImplicitInvocation {
		t.Errorf("AllowImplicitInvocation = %v, want pointer to false", s.Policy.AllowImplicitInvocation)
	}
	if len(s.Policy.Products) != 1 || s.Policy.Products[0] != "odybox" {
		t.Errorf("Products = %v", s.Policy.Products)
	}
	if s.Interface.DisplayName != "Full Skill" || s.Interface.BrandColor != "#ff0000" || s.Interface.DefaultPrompt != "Do the thing" {
		t.Errorf("Interface = %+v", s.Interface)
	}
	if len(s.Dependencies.Skills) != 1 || s.Dependencies.Skills[0] != "base-skill" {
		t.Errorf("Dependencies.Skills = %v", s.Dependencies.Skills)
	}
	if len(s.Dependencies.Tools) != 1 || s.Dependencies.Tools[0].Type != "mcp" || s.Dependencies.Tools[0].Value != "filesystem" {
		t.Errorf("Dependencies.Tools = %+v", s.Dependencies.Tools)
	}
}

func TestIsModelInvocable(t *testing.T) {
	base := &Skill{Name: "x", Type: SkillTypeInline}
	if !base.IsModelInvocable("default") {
		t.Error("inline skill should be model-invocable in default mode")
	}
	hidden := &Skill{Name: "x", Type: SkillTypeInline, HiddenInModes: []string{"product"}}
	if hidden.IsModelInvocable("product") {
		t.Error("hidden-in-mode skill must not be model-invocable in that mode")
	}
	if !hidden.IsModelInvocable("default") {
		t.Error("hidden-in-mode skill should stay invocable in other modes")
	}
	disabled := &Skill{Name: "x", Type: SkillTypeInline, DisableModelInvocation: true}
	if disabled.IsModelInvocable("default") {
		t.Error("disable_model_invocation must block model invocation")
	}
	knowledge := &Skill{Name: "x", Type: SkillTypeKnowledge}
	if knowledge.IsModelInvocable("default") {
		t.Error("knowledge skills are auto-injected, not model-invocable")
	}
	flow := &Skill{Name: "x", Type: SkillTypeFlow}
	if flow.IsModelInvocable("default") {
		t.Error("flow skills run through the flow runtime, not model invocation")
	}
}

func TestAllowsImplicitInvocation(t *testing.T) {
	def := &Skill{Name: "x"}
	if !def.AllowsImplicitInvocation() {
		t.Error("nil policy must default to allowing implicit invocation")
	}
	f := false
	s := &Skill{Name: "x", Policy: SkillPolicy{AllowImplicitInvocation: &f}}
	if s.AllowsImplicitInvocation() {
		t.Error("explicit false must disallow implicit invocation")
	}
}

func TestMatchesProducts(t *testing.T) {
	open := &Skill{Name: "x"}
	if !open.MatchesProducts([]string{"odybox"}) {
		t.Error("empty product restriction must match every product")
	}
	gated := &Skill{Name: "x", Policy: SkillPolicy{Products: []string{"odybox"}}}
	if !gated.MatchesProducts([]string{"odybox"}) {
		t.Error("listed product must match")
	}
	if gated.MatchesProducts([]string{"ody"}) {
		t.Error("unlisted product must not match")
	}
	if gated.MatchesProducts(nil) {
		t.Error("no product context must not match a gated skill")
	}
}

func TestMatchesTrigger(t *testing.T) {
	s := &Skill{Name: "x", Triggers: []string{"deploy", "roll out"}}
	if !s.MatchesTrigger("please deploy this now") {
		t.Error("substring trigger should match")
	}
	if !s.MatchesTrigger("can we ROLL OUT tonight?") {
		t.Error("trigger match must be case-insensitive")
	}
	if s.MatchesTrigger("nothing relevant") {
		t.Error("non-matching text must not trigger")
	}
	if (&Skill{Name: "y"}).MatchesTrigger("deploy") {
		t.Error("skill without triggers never matches")
	}
}
