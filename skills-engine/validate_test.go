package skills

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNameTooLong(t *testing.T) {
	long := strings.Repeat("a", MaxNameLen+1)
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "s"), fmt.Sprintf(`---
name: %s
description: d
---
Body.
`, long))
	if _, err := ParseSkillFile(path); err == nil {
		t.Fatal("expected error for over-long name")
	}
}

func TestValidateDescriptionTooLong(t *testing.T) {
	long := strings.Repeat("d", MaxDescriptionLen+1)
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "s"), fmt.Sprintf(`---
name: ok
description: %s
---
Body.
`, long))
	if _, err := ParseSkillFile(path); err == nil {
		t.Fatal("expected error for over-long description")
	}
}

func TestValidateTooManySkillDependencies(t *testing.T) {
	var deps strings.Builder
	for i := 0; i < MaxSkillDependencyCount+1; i++ {
		fmt.Fprintf(&deps, "    - dep-%d\n", i)
	}
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "s"), `---
name: ok
description: d
dependencies:
  skills:
`+deps.String()+`---
Body.
`)
	if _, err := ParseSkillFile(path); err == nil {
		t.Fatal("expected error for too many skill dependencies")
	}
}

func TestValidateAcceptsBoundaryValues(t *testing.T) {
	name := strings.Repeat("a", MaxNameLen)
	desc := strings.Repeat("d", MaxDescriptionLen)
	path := writeSkillFile(t, filepath.Join(t.TempDir(), "s"), fmt.Sprintf(`---
name: %s
description: %s
---
Body.
`, name, desc))
	if _, err := ParseSkillFile(path); err != nil {
		t.Fatalf("boundary values must parse: %v", err)
	}
}
