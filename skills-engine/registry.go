package skills

import (
	"fmt"
	"sort"
	"sync"
)

// Registry holds the set of known skills keyed by Name and tracks
// which ones are currently active for the session.
type Registry struct {
	mu     sync.RWMutex
	skills map[string]*Skill
	active map[string]bool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
		active: make(map[string]bool),
	}
}

// Add inserts or replaces a skill by name.
func (r *Registry) Add(s *Skill) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[s.Name] = s
}

// Get returns the skill with the given name (nil if missing).
func (r *Registry) Get(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// All returns every registered skill, sorted by name.
func (r *Registry) All() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Activate marks a skill as active for the current session. Returns
// an error if the name is unknown.
func (r *Registry) Activate(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.skills[name]; !ok {
		return fmt.Errorf("skills: unknown skill %q", name)
	}
	r.active[name] = true
	return nil
}

// Deactivate clears the active flag for a skill.
func (r *Registry) Deactivate(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, name)
}

// Active returns the currently active skills, sorted by name.
func (r *Registry) Active() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.active))
	for name := range r.active {
		if s, ok := r.skills[name]; ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ApplyConfig sets the registry's active set to "all known skills
// minus disabled". It replaces whatever was previously active. Names
// in disabled that don't correspond to a registered skill are silently
// ignored — this mirrors Python's behavior and keeps startup resilient
// against stale config entries.
func (r *Registry) ApplyConfig(disabled []string) {
	drop := make(map[string]struct{}, len(disabled))
	for _, n := range disabled {
		drop[n] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active = make(map[string]bool, len(r.skills))
	for name := range r.skills {
		if _, off := drop[name]; off {
			continue
		}
		r.active[name] = true
	}
}
