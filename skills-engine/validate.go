package skills

import "fmt"

// Field limits, mirroring the ody skill loader. They exist so a malformed
// or hostile SKILL.md cannot bloat the prompt catalog or the in-memory
// registry: every value that ends up in a system prompt is bounded.
const (
	// MaxNameLen bounds the front-matter name.
	MaxNameLen = 64
	// MaxDescriptionLen bounds the front-matter description.
	MaxDescriptionLen = 1024
	// MaxShortDescriptionLen bounds interface.short_description.
	MaxShortDescriptionLen = MaxDescriptionLen
	// MaxDefaultPromptLen bounds interface.default_prompt.
	MaxDefaultPromptLen = MaxDescriptionLen
	// MaxSkillDependencyCount bounds dependencies.skills entries.
	MaxSkillDependencyCount = 32
)

// validate enforces the field limits on a freshly parsed skill.
func (s *Skill) validate(path string) error {
	if len(s.Name) > MaxNameLen {
		return fmt.Errorf("skills: %s: name exceeds %d chars (%d)", path, MaxNameLen, len(s.Name))
	}
	if len(s.Description) > MaxDescriptionLen {
		return fmt.Errorf("skills: %s: description exceeds %d chars (%d)", path, MaxDescriptionLen, len(s.Description))
	}
	if len(s.Interface.ShortDescription) > MaxShortDescriptionLen {
		return fmt.Errorf("skills: %s: interface.short_description exceeds %d chars (%d)", path, MaxShortDescriptionLen, len(s.Interface.ShortDescription))
	}
	if len(s.Interface.DefaultPrompt) > MaxDefaultPromptLen {
		return fmt.Errorf("skills: %s: interface.default_prompt exceeds %d chars (%d)", path, MaxDefaultPromptLen, len(s.Interface.DefaultPrompt))
	}
	if len(s.Dependencies.Skills) > MaxSkillDependencyCount {
		return fmt.Errorf("skills: %s: dependencies.skills exceeds %d entries (%d)", path, MaxSkillDependencyCount, len(s.Dependencies.Skills))
	}
	return nil
}
