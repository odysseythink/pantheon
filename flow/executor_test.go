package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	skills "github.com/odysseythink/pantheon/skills-engine"
)

// stubAgent records prompts and answers with a canned function.
type stubAgent struct {
	mu      sync.Mutex
	prompts []string
	answer  func(prompt string) (string, error)
}

func (s *stubAgent) run(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	return s.answer(prompt)
}

func plan(phases ...skills.FlowPhase) *skills.FlowPlan {
	return &skills.FlowPlan{Phases: phases}
}

func TestRunSequentialStepsWithInterpolation(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if strings.Contains(prompt, "research") {
			return "go is great", nil
		}
		return "summary: " + prompt, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	res, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "research ${{ input }}", Output: "findings"},
			{Agent: "summarize ${{ findings }}", Output: "summary"},
		},
	}), "odybox")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[0]; got != "research odybox" {
		t.Errorf("prompt[0] = %q, want input interpolated", got)
	}
	if got := stub.prompts[1]; got != "summarize go is great" {
		t.Errorf("prompt[1] = %q, want findings interpolated", got)
	}
	if res.Outputs["findings"] != "go is great" {
		t.Errorf("Outputs[findings] = %q", res.Outputs["findings"])
	}
	if res.Final != res.Outputs["summary"] {
		t.Errorf("Final = %q, want the last step output", res.Final)
	}
}

func TestRunUndefinedBindingFails(t *testing.T) {
	ex := &Executor{RunAgent: (&stubAgent{answer: func(string) (string, error) { return "", nil }}).run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "use ${{ missing }}"}},
	}), "")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected undefined-binding error naming the key, got %v", err)
	}
}

func TestRunAgentErrorPropagates(t *testing.T) {
	boom := errors.New("model exploded")
	ex := &Executor{RunAgent: (&stubAgent{answer: func(string) (string, error) { return "", boom }}).run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID:    "main",
		Steps: []skills.FlowStep{{Agent: "anything"}},
	}), "")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
}

func TestRunParallelSteps(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if strings.HasPrefix(prompt, "slow") {
			time.Sleep(50 * time.Millisecond)
		}
		return "done:" + prompt, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	start := time.Now()
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Parallel: []skills.FlowStep{
				{Agent: "slow a", Output: "ra"},
				{Agent: "slow b", Output: "rb"},
			}},
			{Agent: "combine ${{ ra }} + ${{ rb }}", Output: "final"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("parallel steps ran sequentially (%v)", elapsed)
	}
	if got := stub.prompts[len(stub.prompts)-1]; got != "combine done:slow a + done:slow b" {
		t.Errorf("combine prompt = %q", got)
	}
}

func TestRunPipelineFanOut(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if prompt == "list topics" {
			return `["go", "rust", "zig"]`, nil
		}
		return "wrote " + prompt, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	res, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "list topics", Output: "topics"},
			{Pipeline: "${{ topics }}", Each: "write about ${{ item }}", Output: "drafts"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	wantPrompts := []string{"write about go", "write about rust", "write about zig"}
	for i, want := range wantPrompts {
		if got := stub.prompts[i+1]; got != want {
			t.Errorf("prompt[%d] = %q, want %q", i+1, got, want)
		}
	}
	drafts, ok := res.Outputs["drafts"].([]string)
	if !ok || len(drafts) != 3 {
		t.Fatalf("drafts = %#v, want 3 strings", res.Outputs["drafts"])
	}
	if drafts[0] != "wrote write about go" {
		t.Errorf("drafts[0] = %q", drafts[0])
	}
	// An array binding interpolates as JSON.
	if res.Final != `["wrote write about go","wrote write about rust","wrote write about zig"]` {
		t.Errorf("Final = %q", res.Final)
	}
}

func TestRunPipelineSplitsPlainTextIntoLines(t *testing.T) {
	stub := &stubAgent{answer: func(prompt string) (string, error) {
		if prompt == "list" {
			return "alpha\nbeta", nil
		}
		return "seen " + prompt, nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "list", Output: "items"},
			{Pipeline: "${{ items }}", Each: "handle ${{ item }}"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stub.prompts[1]; got != "handle alpha" {
		t.Errorf("prompt[1] = %q", got)
	}
	if got := stub.prompts[2]; got != "handle beta" {
		t.Errorf("prompt[2] = %q", got)
	}
}

func TestRunPipelineBatchLimit(t *testing.T) {
	items := make([]string, BatchLimit+1)
	for i := range items {
		items[i] = fmt.Sprintf("\"item-%d\"", i)
	}
	stub := &stubAgent{answer: func(string) (string, error) {
		return "[" + strings.Join(items, ",") + "]", nil
	}}
	ex := &Executor{RunAgent: stub.run}
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "list", Output: "items"},
			{Pipeline: "${{ items }}", Each: "handle ${{ item }}"},
		},
	}), "")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected batch-limit error, got %v", err)
	}
}

func TestRunEmitsStepEvents(t *testing.T) {
	ex := &Executor{RunAgent: (&stubAgent{answer: func(string) (string, error) { return "ok", nil }}).run}
	var events []StepEvent
	ex.OnStep = func(ev StepEvent) { events = append(events, ev) }
	_, err := ex.Run(context.Background(), plan(skills.FlowPhase{
		ID: "main",
		Steps: []skills.FlowStep{
			{Agent: "a", Output: "x"},
			{Agent: "b"},
		},
	}), "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %v, want 4 (2 started + 2 finished)", events)
	}
	if events[0].Status != StepStarted || events[0].PhaseID != "main" || events[0].Kind != "agent" {
		t.Errorf("events[0] = %+v", events[0])
	}
	if events[3].Status != StepFinished {
		t.Errorf("events[3] = %+v", events[3])
	}
}
