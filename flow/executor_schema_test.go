package flow

import (
	"context"
	"strings"
	"testing"

	skills "github.com/odysseythink/pantheon/skills-engine"
)

// Schema-constrained agent steps (ody-rs M2.4): the schema is injected at
// the prompt tail, the reply must be JSON satisfying the schema subset, and
// validation failures feed back into a retry prompt up to 3 attempts.

var testSchema = map[string]any{
	"type":     "object",
	"required": []any{"name"},
	"properties": map[string]any{
		"name": map[string]any{"type": "string"},
	},
}

func TestRunAgentSchemaInjectsInstructionAndBindsParsedJSON(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) {
		return `{"name": "odybox", "extra": 1}`, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	res, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "describe the thing", Output: "info", Schema: testSchema},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stub.prompts[0], "Reply with JSON only") || !strings.Contains(stub.prompts[0], `"required"`) {
		t.Errorf("prompt must carry the schema instruction, got %q", stub.prompts[0])
	}
	info, ok := res.Outputs["info"].(map[string]any)
	if !ok || info["name"] != "odybox" {
		t.Errorf("Outputs[info] = %#v, want parsed object", res.Outputs["info"])
	}
}

func TestRunAgentSchemaRetryFeedsBackFailureReason(t *testing.T) {
	calls := 0
	stub := &stubAgent{answer: func(string) (string, error) {
		calls++
		if calls == 1 {
			return "not json at all", nil
		}
		return `{"name": "fixed"}`, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "describe", Output: "info", Schema: testSchema}},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (initial + 1 retry)", calls)
	}
	if !strings.Contains(stub.prompts[1], "failed validation") || !strings.Contains(stub.prompts[1], "not valid JSON") {
		t.Errorf("retry prompt must carry the failure reason, got %q", stub.prompts[1])
	}
}

func TestRunAgentSchemaFailsAfterMaxAttempts(t *testing.T) {
	calls := 0
	stub := &stubAgent{answer: func(string) (string, error) {
		calls++
		return `{"wrong": true}`, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "describe", Output: "info", Schema: testSchema}},
	}), "")
	if err == nil || !strings.Contains(err.Error(), "schema validation failed after 3 attempts") {
		t.Fatalf("err = %v, want max-attempts failure", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRunAgentWithoutSchemaKeepsRawTextBinding(t *testing.T) {
	stub := &stubAgent{answer: func(string) (string, error) {
		return "plain prose answer", nil
	}}
	ex := &Executor{RunAgent: stub.run}
	res, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "chat", Output: "out"}},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Outputs["out"] != "plain prose answer" {
		t.Errorf("Outputs[out] = %#v, want raw string", res.Outputs["out"])
	}
}
