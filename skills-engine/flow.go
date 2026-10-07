package skills

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FlowArtifactFilename is the declarative workflow plan a flow-type skill
// carries next to its SKILL.md. It is parsed and validated eagerly at load
// time; executing the plan is the host's job (e.g. pantheon's flow package).
const FlowArtifactFilename = "flow.yaml"

// FlowPlan is a declarative multi-step workflow: ordered phases, each with
// steps that run agents sequentially, concurrently, or fanned out over the
// items of a previous output. Steps bind their result to a name via
// `output`; later steps reference it with `${{ name }}` interpolation.
type FlowPlan struct {
	Phases []FlowPhase `yaml:"phases"`
}

// FlowPhase groups steps under a stable id (used in progress reporting).
type FlowPhase struct {
	ID    string     `yaml:"id"`
	Steps []FlowStep `yaml:"steps"`
}

// FlowStep is exactly one of: an agent invocation, a pipeline fan-out, or
// a parallel block of sub-steps.
type FlowStep struct {
	// Agent runs one agent with the step prompt; the agent name is resolved
	// by the host executor.
	Agent string `yaml:"agent,omitempty"`
	// Pipeline fans out one agent (named by Each) per item produced by the
	// Pipeline expression (usually a ${{ output }} reference).
	Pipeline string `yaml:"pipeline,omitempty"`
	Each     string `yaml:"each,omitempty"`
	// Parallel runs sub-steps concurrently and waits for all of them.
	Parallel []FlowStep `yaml:"parallel,omitempty"`
	// Output binds the step result to a name for later interpolation.
	Output string `yaml:"output,omitempty"`
}

// Kind reports which of the three step shapes is set ("agent", "pipeline",
// "parallel"), or "" when none is.
func (s FlowStep) Kind() string {
	switch {
	case s.Agent != "":
		return "agent"
	case s.Pipeline != "":
		return "pipeline"
	case len(s.Parallel) > 0:
		return "parallel"
	}
	return ""
}

// loadFlowArtifact reads and validates the flow.yaml next to the given
// SKILL.md. A missing artifact or an invalid plan is a load error: flow
// skills are only useful when their plan is sound, so failing fast beats
// discovering the problem mid-run.
func loadFlowArtifact(skillPath string) (*FlowPlan, error) {
	artifact := filepath.Join(filepath.Dir(skillPath), FlowArtifactFilename)
	raw, err := os.ReadFile(artifact)
	if err != nil {
		return nil, fmt.Errorf("skills: %s: flow skill requires %s: %w", skillPath, FlowArtifactFilename, err)
	}
	var plan FlowPlan
	if err := yaml.Unmarshal(raw, &plan); err != nil {
		return nil, fmt.Errorf("skills: %s: invalid %s: %w", skillPath, FlowArtifactFilename, err)
	}
	if err := plan.validate(skillPath); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (p *FlowPlan) validate(skillPath string) error {
	errf := func(format string, args ...any) error {
		return fmt.Errorf("skills: %s: %s: %s", skillPath, FlowArtifactFilename, fmt.Sprintf(format, args...))
	}
	if len(p.Phases) == 0 {
		return errf("phases must not be empty")
	}
	seen := map[string]bool{}
	for i, phase := range p.Phases {
		if phase.ID == "" {
			return errf("phases[%d]: id is required", i)
		}
		if seen[phase.ID] {
			return errf("duplicate phase id %q", phase.ID)
		}
		seen[phase.ID] = true
		for j, step := range phase.Steps {
			if err := validateStep(step); err != nil {
				return errf("phase %q step %d: %v", phase.ID, j, err)
			}
		}
	}
	return nil
}

func validateStep(s FlowStep) error {
	kinds := 0
	for _, set := range []bool{s.Agent != "", s.Pipeline != "", len(s.Parallel) > 0} {
		if set {
			kinds++
		}
	}
	if kinds == 0 {
		return fmt.Errorf("step must declare one of agent/pipeline/parallel")
	}
	if kinds > 1 {
		return fmt.Errorf("step must declare exactly one of agent/pipeline/parallel")
	}
	if s.Pipeline != "" && s.Each == "" {
		return fmt.Errorf("pipeline step requires each")
	}
	if s.Each != "" && s.Pipeline == "" {
		return fmt.Errorf("each is only valid on pipeline steps")
	}
	for i, sub := range s.Parallel {
		if err := validateStep(sub); err != nil {
			return fmt.Errorf("parallel[%d]: %v", i, err)
		}
	}
	return nil
}
