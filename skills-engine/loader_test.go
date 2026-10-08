package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkipsSkillsBeyondMaxDepth(t *testing.T) {
	root := t.TempDir()
	// Depth 6 from the root: <root>/d1/d2/d3/d4/d5/d6/SKILL.md — kept.
	deep := filepath.Join(root, "d1", "d2", "d3", "d4", "d5", "d6")
	writeSkillFile(t, deep, "---\nname: deep-ok\ndescription: d\n---\nBody.\n")
	// Depth 7 — beyond the cap, must be skipped.
	deeper := filepath.Join(deep, "d7")
	writeSkillFile(t, deeper, "---\nname: too-deep\ndescription: d\n---\nBody.\n")

	loaded, _ := NewLoader(root).Load()
	names := map[string]bool{}
	for _, s := range loaded {
		names[s.Name] = true
	}
	if !names["deep-ok"] {
		t.Error("skill at exactly max depth must be loaded")
	}
	if names["too-deep"] {
		t.Error("skill beyond max depth must be skipped")
	}
}

func TestLoadHonoursDirCap(t *testing.T) {
	defer func(old int) { MaxDirsPerRoot = old }(MaxDirsPerRoot)
	MaxDirsPerRoot = 5

	root := t.TempDir()
	for i := 0; i < 8; i++ {
		writeSkillFile(t, filepath.Join(root, fmt.Sprintf("skill-%02d", i)),
			fmt.Sprintf("---\nname: s%02d\ndescription: d\n---\nBody.\n", i))
	}
	loaded, errs := NewLoader(root).Load()
	if len(loaded) > MaxDirsPerRoot {
		t.Errorf("loaded %d skills, dir cap is %d", len(loaded), MaxDirsPerRoot)
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Err.Error(), "dir cap") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a dir-cap LoadError, got %v", errs)
	}
}

func TestLoadSurfacesMalformedSkillErrors(t *testing.T) {
	root := t.TempDir()
	// Malformed: no front-matter.
	bad := filepath.Join(root, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("no frontmatter here"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, errs := NewLoader(root).Load()
	if len(errs) != 1 {
		t.Fatalf("expected 1 LoadError, got %d", len(errs))
	}
	if !strings.HasSuffix(errs[0].Path, "SKILL.md") {
		t.Errorf("LoadError.Path = %q, want the SKILL.md path", errs[0].Path)
	}
}
