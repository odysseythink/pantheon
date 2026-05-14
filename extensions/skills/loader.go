package skills

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Loader walks a skills home directory and returns Skill pointers.
// Each skill lives at <home>/<category>/<name>/SKILL.md. Categories
// are purely cosmetic — the loader flattens everything into a single
// list keyed by Skill.Name.
type Loader struct {
	Home string
}

// NewLoader constructs a Loader rooted at home.
func NewLoader(home string) *Loader {
	return &Loader{Home: home}
}

// LoadError captures a malformed skill file encountered during loading.
type LoadError struct {
	Path string
	Err  error
}

// Load walks the skills home and returns every SKILL.md it finds,
// sorted by name. Malformed files are skipped (the error is captured
// on the returned slice alongside the path so callers can warn).
func (l *Loader) Load() ([]*Skill, []LoadError) {
	var skills []*Skill
	var errs []LoadError
	_ = filepath.WalkDir(l.Home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Ignore walk errors; most often the home directory
			// doesn't exist yet, which is fine.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "SKILL.md") {
			s, perr := ParseSkillFile(path)
			if perr != nil {
				errs = append(errs, LoadError{Path: path, Err: perr})
				return nil
			}
			skills = append(skills, s)
		}
		return nil
	})
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, errs
}
