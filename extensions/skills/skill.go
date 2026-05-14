// Package skills parses, loads, and manages markdown-based AI skill
// packs. A skill is a directory containing a SKILL.md file whose YAML
// front-matter describes the skill (name, description, commands, tools).
package skills

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

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

	// Raw path to the SKILL.md file on disk.
	Path string `yaml:"-"`

	// Body is the markdown content that follows the YAML front-matter.
	// Used as the skill's system-prompt contribution when activated.
	Body string `yaml:"-"`
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
	return &s, nil
}
