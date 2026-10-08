// Package skills parses, loads, and manages markdown-based AI skill
// packs. A skill is a directory containing a SKILL.md file whose YAML
// front-matter describes the skill (name, description, commands, tools).
//
// Beyond the basic fields, the engine understands the richer metadata
// model popularized by the ody skills runtime: skill types (prompt,
// inline, flow, knowledge), trigger words, per-mode visibility, model
// invocation gating, product scoping, presentation metadata and
// declared dependencies on other skills or tools.
package skills

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillType classifies how a skill is activated at runtime.
type SkillType string

const (
	// SkillTypePrompt is injected as a system reminder when explicitly activated.
	SkillTypePrompt SkillType = "prompt"
	// SkillTypeInline can be invoked by user slash or the model Skill tool.
	// It is the default when the front-matter omits `type`.
	SkillTypeInline SkillType = "inline"
	// SkillTypeFlow is a multi-step workflow skill executed by a host-side
	// flow runtime (declared via a flow.yaml artifact next to SKILL.md).
	SkillTypeFlow SkillType = "flow"
	// SkillTypeKnowledge is auto-injected when its triggers match user text.
	SkillTypeKnowledge SkillType = "knowledge"
)

func validSkillType(t SkillType) bool {
	switch t {
	case SkillTypePrompt, SkillTypeInline, SkillTypeFlow, SkillTypeKnowledge:
		return true
	}
	return false
}

// SkillPolicy controls how the skill may be triggered and which products
// it belongs to. An empty Products list means "no restriction".
type SkillPolicy struct {
	// AllowImplicitInvocation gates trigger-based (implicit) activation.
	// nil means "allow" — the ody default.
	AllowImplicitInvocation *bool `yaml:"allow_implicit_invocation,omitempty"`
	// Products scopes the skill to named products (e.g. "ody", "odybox").
	Products []string `yaml:"products,omitempty"`
}

// SkillInterface carries presentation metadata for dashboards and pickers.
type SkillInterface struct {
	DisplayName      string `yaml:"display_name,omitempty"`
	ShortDescription string `yaml:"short_description,omitempty"`
	IconSmall        string `yaml:"icon_small,omitempty"`
	IconLarge        string `yaml:"icon_large,omitempty"`
	BrandColor       string `yaml:"brand_color,omitempty"`
	DefaultPrompt    string `yaml:"default_prompt,omitempty"`
}

// SkillDependencies declares what else a skill needs to function.
type SkillDependencies struct {
	Tools  []SkillToolDependency `yaml:"tools,omitempty"`
	Skills []string              `yaml:"skills,omitempty"`
}

// SkillToolDependency is a declared dependency on an external tool.
type SkillToolDependency struct {
	Type        string `yaml:"type,omitempty"`
	Value       string `yaml:"value,omitempty"`
	Description string `yaml:"description,omitempty"`
	Transport   string `yaml:"transport,omitempty"`
	Command     string `yaml:"command,omitempty"`
	URL         string `yaml:"url,omitempty"`
}

// Skill describes a single installed skill package.
type Skill struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version,omitempty"`
	Author      string   `yaml:"author,omitempty"`
	License     string   `yaml:"license,omitempty"`
	Commands    []string `yaml:"commands,omitempty"` // slash commands this skill registers
	Tools       []string `yaml:"tools,omitempty"`    // tool names this skill wants injected
	Tags        []string `yaml:"-"`                  // convenience — populated from metadata.hermes.tags

	// Runtime metadata (ody-compatible).
	Type                   SkillType         `yaml:"type,omitempty"`
	Triggers               []string          `yaml:"triggers,omitempty"`
	HiddenInModes          []string          `yaml:"hidden_in_modes,omitempty"`
	DisableModelInvocation bool              `yaml:"disable_model_invocation,omitempty"`
	Policy                 SkillPolicy       `yaml:"policy,omitempty"`
	Interface              SkillInterface    `yaml:"interface,omitempty"`
	Dependencies           SkillDependencies `yaml:"dependencies,omitempty"`

	// Raw path to the SKILL.md file on disk.
	Path string `yaml:"-"`

	// Body is the markdown content that follows the YAML front-matter.
	// Used as the skill's system-prompt contribution when activated.
	Body string `yaml:"-"`

	// Scope and Root describe where the skill was discovered; populated
	// by LoadRoots (zero values when loaded via Loader.Load).
	Scope Scope  `yaml:"-"`
	Root  string `yaml:"-"`

	// Flow is the parsed flow.yaml plan; only set for flow-type skills.
	Flow *FlowPlan `yaml:"-"`
}

// IsModelInvocable reports whether the model may explicitly invoke this
// skill in the given runtime mode. Knowledge and Flow skills are never
// model-invocable: the former are auto-injected by trigger matching, the
// latter run through the flow runtime.
func (s *Skill) IsModelInvocable(mode string) bool {
	if s.DisableModelInvocation {
		return false
	}
	for _, m := range s.HiddenInModes {
		if m == mode {
			return false
		}
	}
	return s.Type == SkillTypeInline || s.Type == SkillTypePrompt
}

// AllowsImplicitInvocation reports whether trigger-based (implicit)
// activation is permitted. Defaults to true when the policy is unset.
func (s *Skill) AllowsImplicitInvocation() bool {
	if s.Policy.AllowImplicitInvocation == nil {
		return true
	}
	return *s.Policy.AllowImplicitInvocation
}

// MatchesProducts reports whether the skill is visible for the given
// product context. An unrestricted skill (no policy products) matches
// everything; a gated skill only matches when one of its products is
// present in the context.
func (s *Skill) MatchesProducts(products []string) bool {
	if len(s.Policy.Products) == 0 {
		return true
	}
	for _, want := range s.Policy.Products {
		for _, have := range products {
			if want == have {
				return true
			}
		}
	}
	return false
}

// MatchesTrigger reports whether any of the skill's trigger words appears
// in text (case-insensitive substring match). Skills without triggers
// never match.
func (s *Skill) MatchesTrigger(text string) bool {
	if len(s.Triggers) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, trig := range s.Triggers {
		if trig = strings.TrimSpace(trig); trig != "" && strings.Contains(lower, strings.ToLower(trig)) {
			return true
		}
	}
	return false
}

// Metadata is a loose typed shape used to pull known keys out of the
// (otherwise free-form) metadata block.
type metadataBlock struct {
	Hermes struct {
		Tags          []string `yaml:"tags"`
		RelatedSkills []string `yaml:"related_skills"`
	} `yaml:"hermes"`
}

// ParseSkillFile reads a SKILL.md-style file and returns the parsed
// Skill. The file must start with a `---` delimited YAML front-matter
// block; the remaining text becomes Body.
func ParseSkillFile(path string) (*Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", path, err)
	}
	return parseSkillBytes(path, raw)
}

func parseSkillBytes(path string, raw []byte) (*Skill, error) {
	if !bytes.HasPrefix(raw, []byte("---")) {
		return nil, fmt.Errorf("skills: %s: missing YAML front matter", path)
	}
	// Find the closing --- on its own line.
	rest := raw[3:]
	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		return nil, fmt.Errorf("skills: %s: unterminated YAML front matter", path)
	}
	front := rest[:idx]
	body := rest[idx+4:]
	// Strip one leading newline from body if present.
	body = bytes.TrimPrefix(body, []byte("\n"))

	var s Skill
	if err := yaml.Unmarshal(front, &s); err != nil {
		return nil, fmt.Errorf("skills: %s: yaml: %w", path, err)
	}
	// Skills in the wild follow the ody-code convention of camelCase keys;
	// accept the hiddenInModes spelling alongside hidden_in_modes.
	var alias struct {
		HiddenInModes []string `yaml:"hiddenInModes"`
	}
	_ = yaml.Unmarshal(front, &alias)
	if len(s.HiddenInModes) == 0 {
		s.HiddenInModes = alias.HiddenInModes
	}
	// Parse metadata separately so we can pull tags without polluting
	// the primary struct.
	var meta struct {
		Metadata metadataBlock `yaml:"metadata"`
	}
	_ = yaml.Unmarshal(front, &meta)
	s.Tags = meta.Metadata.Hermes.Tags
	s.Path = path
	s.Body = string(body)
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("skills: %s: missing required 'name' field", path)
	}
	if s.Type == "" {
		s.Type = SkillTypeInline
	}
	if !validSkillType(s.Type) {
		return nil, fmt.Errorf("skills: %s: invalid type %q (want prompt|inline|flow|knowledge)", path, s.Type)
	}
	if s.Type == SkillTypeFlow {
		plan, err := loadFlowArtifact(path)
		if err != nil {
			return nil, err
		}
		s.Flow = plan
	}
	if err := s.validate(path); err != nil {
		return nil, err
	}
	return &s, nil
}
