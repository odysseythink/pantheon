package flow

import (
	"context"
	"strings"
	"testing"

	skills "github.com/odysseythink/pantheon/skills-engine"
)

// Path interpolation (ody-rs interp.rs): ${{ name.path }} walks objects and
// arrays (numeric segments index arrays); inside a pipeline each template
// both ${{ item.path }} and the single-brace ${item.path} forms resolve
// against the current item.

func TestRunPathInterpolationObjectAndArray(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if strings.HasPrefix(prompt, "survey") {
			return `{"total": 2, "items": ["a", "b"]}`, nil
		}
		return "done", nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "survey", Output: "findings"},
			{Agent: "report total=${{ findings.total }} second=${{ findings.items.1 }}"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[1]; got != "report total=2 second=b" {
		t.Errorf("prompt[1] = %q, want path-interpolated values", got)
	}
}

func TestRunPathInterpolationMissingPathFails(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) {
		return `{"total": 2}`, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "survey", Output: "findings"},
			{Agent: "report ${{ findings.nope }}"},
		},
	}), "")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want missing-path error naming the segment", err)
	}
}

func TestRunPipelineItemPathInterpolation(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if prompt == "list" {
			return `[{"name": "x", "rank": 1}, {"name": "y", "rank": 2}]`, nil
		}
		return "seen", nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "list", Output: "rows"},
			{Pipeline: "${{ rows }}", Each: "handle ${{ item.name }} rank ${item.rank}"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[1]; got != "handle x rank 1" {
		t.Errorf("prompt[1] = %q, want item path interpolation", got)
	}
	if got := stub.prompts[2]; got != "handle y rank 2" {
		t.Errorf("prompt[2] = %q, want item path interpolation", got)
	}
}

func TestRunPipelineExpressionResolvesStructuredPath(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if prompt == "list" {
			return `{"topics": ["go", "rust"]}`, nil
		}
		return "seen", nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "list", Output: "data"},
			{Pipeline: "${{ data.topics }}", Each: "write about ${{ item }}"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[1]; got != "write about go" {
		t.Errorf("prompt[1] = %q", got)
	}
	if got := stub.prompts[2]; got != "write about rust" {
		t.Errorf("prompt[2] = %q", got)
	}
}

func TestRunSingleBraceFormOutsidePipelineStaysLiteral(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) { return "ok", nil }}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "shell echo ${HOME} and ${item}"}},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[0]; got != "shell echo ${HOME} and ${item}" {
		t.Errorf("prompt = %q, want single-brace forms untouched outside pipelines", got)
	}
}
