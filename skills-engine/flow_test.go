package skills

import (
	"os"
	"path/filepath"
	"testing"
)

const flowSkillMD = `---
name: release-flow
description: ship a release
type: flow
---
Follow the flow plan.
`

const validFlowYAML = `phases:
  - id: research
    steps:
      - agent: researcher
        output: findings
      - parallel:
          - agent: fact-checker
            output: facts
          - agent: summarizer
  - id: write
    steps:
      - pipeline: "${{ findings }}"
        each: writer
        output: drafts
`

func writeFlowSkill(t *testing.T, flowYAML *string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "release-flow")
	path := writeSkillFile(t, dir, flowSkillMD)
	if flowYAML != nil {
		if err := os.WriteFile(filepath.Join(dir, "flow.yaml"), []byte(*flowYAML), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestFlowSkillLoadsPlan(t *testing.T) {
	yaml := validFlowYAML
	s, err := ParseSkillFile(writeFlowSkill(t, &yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Flow == nil {
		t.Fatal("flow skill must carry the parsed plan")
	}
	if len(s.Flow.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(s.Flow.Phases))
	}
	research := s.Flow.Phases[0]
	if research.ID != "research" || len(research.Steps) != 2 {
		t.Fatalf("phase[0] = %+v", research)
	}
	if research.Steps[0].Agent != "researcher" || research.Steps[0].Output != "findings" {
		t.Errorf("step[0] = %+v", research.Steps[0])
	}
	par := research.Steps[1]
	if len(par.Parallel) != 2 || par.Parallel[0].Agent != "fact-checker" {
		t.Errorf("parallel step = %+v", par)
	}
	pipe := s.Flow.Phases[1].Steps[0]
	if pipe.Pipeline != "${{ findings }}" || pipe.Each != "writer" || pipe.Output != "drafts" {
		t.Errorf("pipeline step = %+v", pipe)
	}
}

func TestFlowSkillWithoutArtifactFails(t *testing.T) {
	if _, err := ParseSkillFile(writeFlowSkill(t, nil)); err == nil {
		t.Fatal("flow skill without flow.yaml must fail to load")
	}
}

func TestNonFlowSkillIgnoresFlowYAML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "inline-skill")
	path := writeSkillFile(t, dir, "---\nname: inline-skill\ndescription: d\n---\nBody.\n")
	if err := os.WriteFile(filepath.Join(dir, "flow.yaml"), []byte("not: a plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseSkillFile(path)
	if err != nil {
		t.Fatalf("inline skill must not validate flow.yaml: %v", err)
	}
	if s.Flow != nil {
		t.Error("inline skill must not carry a flow plan")
	}
}

func TestFlowPlanValidation(t *testing.T) {
	cases := map[string]string{
		"empty phases": `phases: []`,
		"phase without id": `phases:
  - steps:
      - agent: a`,
		"step without kind": `phases:
  - id: p
    steps:
      - output: x`,
		"step with two kinds": `phases:
  - id: p
    steps:
      - agent: a
        pipeline: "${{ x }}"
        each: b`,
		"pipeline without each": `phases:
  - id: p
    steps:
      - pipeline: "${{ x }}"`,
		"each without pipeline": `phases:
  - id: p
    steps:
      - agent: a
        each: b`,
		"duplicate phase id": `phases:
  - id: p
    steps:
      - agent: a
  - id: p
    steps:
      - agent: b`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			y := yaml
			if _, err := ParseSkillFile(writeFlowSkill(t, &y)); err == nil {
				t.Fatalf("invalid plan %q must fail to load", name)
			}
		})
	}
}
