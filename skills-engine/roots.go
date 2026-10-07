package skills

import "sort"

// Root is one skill discovery directory plus the scope its skills belong
// to. Pass roots to LoadRoots in precedence order: the first root that
// provides a skill name wins, so more specific layers (project) shadow
// broader ones (user, system) — the ody layering model.
type Root struct {
	Path  string
	Scope Scope
}

// LoadOutcome is the merged result of loading every root: the deduped
// skill set plus every error encountered along the way. Errors never
// discard valid skills — a malformed SKILL.md in one root must not hide
// working skills from another.
type LoadOutcome struct {
	Skills []*Skill
	Errors []LoadError
}

// LoadRoots loads each root with Loader and merges the results. The first
// root to supply a given skill name wins (shadowing); every returned skill
// is tagged with the Scope and Root path it was discovered in.
func LoadRoots(roots ...Root) *LoadOutcome {
	out := &LoadOutcome{}
	seen := map[string]bool{}
	for _, root := range roots {
		loaded, errs := NewLoader(root.Path).Load()
		out.Errors = append(out.Errors, errs...)
		for _, s := range loaded {
			if s == nil || s.Name == "" || seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			s.Scope = root.Scope
			s.Root = root.Path
			out.Skills = append(out.Skills, s)
		}
	}
	sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Name < out.Skills[j].Name })
	return out
}
