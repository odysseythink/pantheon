package flow

import (
	"context"
	"strings"
	"testing"

	skills "github.com/odysseythink/pantheon/skills-engine"
)

// Structured trigger inputs (ody-rs args root): RunWithArgs pre-binds the
// args map so templates can address ${{ args.key }} with full path support.

func TestRunWithArgsBindsArgsRoot(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) { return "ok", nil }}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.RunWithArgs(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "调研 ${{ args.topic }} 共 ${{ args.limit }} 项"}},
	}), "fallback task", map[string]any{"topic": "odybox", "limit": float64(3)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[0]; got != "调研 odybox 共 3 项" {
		t.Errorf("prompt = %q, want args interpolated", got)
	}
}

func TestRunWithArgsNestedPath(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) { return "ok", nil }}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.RunWithArgs(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "lang=${{ args.filter.lang }}"}},
	}), "", map[string]any{"filter": map[string]any{"lang": "go"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[0]; got != "lang=go" {
		t.Errorf("prompt = %q, want nested args path", got)
	}
}

func TestRunWithArgsMissingKeyFails(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) { return "ok", nil }}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.RunWithArgs(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "use ${{ args.b }}"}},
	}), "", map[string]any{"a": float64(1)})
	if err == nil || !strings.Contains(err.Error(), "b") {
		t.Fatalf("err = %v, want missing-args-key error naming the key", err)
	}
}

func TestRunWithoutArgsRendersEmptyArgsObject(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) { return "ok", nil }}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "ctx=${{ args }}"}},
	}), "task")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[0]; got != "ctx={}" {
		t.Errorf("prompt = %q, want empty args object", got)
	}
}
