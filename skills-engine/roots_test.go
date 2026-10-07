package skills

import (
	"fmt"
	"path/filepath"
	"testing"
)

func makeRoot(t *testing.T, scope Scope, names ...string) Root {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		writeSkillFile(t, filepath.Join(dir, n),
			fmt.Sprintf("---\nname: %s\ndescription: from %s\n---\nBody of %s.\n", n, scope, n))
	}
	return Root{Path: dir, Scope: scope}
}

func TestLoadRootsTagsScopeAndRoot(t *testing.T) {
	root := makeRoot(t, ScopeProject, "alpha")
	out := LoadRoots(root)
	if len(out.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", out.Errors)
	}
	s := out.Skills[0]
	if s.Scope != ScopeProject {
		t.Errorf("Scope = %q, want %q", s.Scope, ScopeProject)
	}
	if s.Root != root.Path {
		t.Errorf("Root = %q, want %q", s.Root, root.Path)
	}
}

func TestLoadRootsFirstRootShadowsByName(t *testing.T) {
	high := makeRoot(t, ScopeProject, "shared", "proj-only")
	low := makeRoot(t, ScopeUser, "shared", "user-only")
	out := LoadRoots(high, low)

	byName := map[string]*Skill{}
	for _, s := range out.Skills {
		byName[s.Name] = s
	}
	if len(byName) != 3 {
		t.Fatalf("expected 3 merged skills, got %d", len(byName))
	}
	if byName["shared"].Scope != ScopeProject {
		t.Errorf("shared skill should come from the first (project) root, got scope %q", byName["shared"].Scope)
	}
	if byName["shared"].Description != "from project" {
		t.Errorf("shared description = %q, want the project copy", byName["shared"].Description)
	}
	if byName["user-only"].Scope != ScopeUser {
		t.Errorf("user-only Scope = %q", byName["user-only"].Scope)
	}
}

func TestLoadRootsCollectsErrorsWithoutDroppingValidSkills(t *testing.T) {
	good := makeRoot(t, ScopeProject, "good")
	badDir := t.TempDir()
	// Malformed skill: unknown type.
	writeSkillFile(t, filepath.Join(badDir, "bad"),
		"---\nname: bad\ndescription: d\ntype: wizard\n---\nBody.\n")

	out := LoadRoots(good, Root{Path: badDir, Scope: ScopeUser})
	if len(out.Skills) != 1 || out.Skills[0].Name != "good" {
		t.Errorf("Skills = %v, want just [good]", out.Skills)
	}
	if len(out.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(out.Errors))
	}
}

func TestLoadRootsSkipsMissingRoots(t *testing.T) {
	out := LoadRoots(Root{Path: filepath.Join(t.TempDir(), "does-not-exist"), Scope: ScopeUser})
	if len(out.Skills) != 0 || len(out.Errors) != 0 {
		t.Errorf("missing root should yield empty outcome, got %+v", out)
	}
}
