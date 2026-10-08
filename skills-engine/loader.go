package skills

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Traversal limits, mirroring the ody skill loader. They bound how much
// damage a pathological skills tree (deep nesting, thousands of dirs) can
// do to load time and memory. MaxDirsPerRoot is a var so tests can lower
// it without creating thousands of directories.
const MaxScanDepth = 6

var MaxDirsPerRoot = 2000

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
// sorted by name. Malformed files are skipped with the error captured on
// the returned slice; directories nested deeper than MaxScanDepth are not
// descended into, and scanning stops with a LoadError once the directory
// count exceeds MaxDirsPerRoot.
func (l *Loader) Load() ([]*Skill, []LoadError) {
	var skills []*Skill
	var errs []LoadError
	dirsSeen := 0
	capHit := false
	_ = filepath.WalkDir(l.Home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Ignore walk errors; most often the home directory
			// doesn't exist yet, which is fine.
			return nil
		}
		if d.IsDir() {
			if path != l.Home {
				dirsSeen++
				if dirsSeen > MaxDirsPerRoot {
					if !capHit {
						capHit = true
						errs = append(errs, LoadError{
							Path: l.Home,
							Err:  fmt.Errorf("skills: %s: dir cap %d exceeded, scan truncated", l.Home, MaxDirsPerRoot),
						})
					}
					return filepath.SkipAll
				}
			}
			if depth := dirDepth(l.Home, path); depth > MaxScanDepth {
				return filepath.SkipDir
			}
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

// dirDepth returns the depth of dir relative to root: root itself is 0,
// <root>/a is 1, <root>/a/b is 2, and so on.
func dirDepth(root, dir string) int {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}
