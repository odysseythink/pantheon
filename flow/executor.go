// Package flow executes the declarative FlowPlans carried by flow-type
// skills (see pantheon/skills-engine). The executor is host-agnostic: it
// renders prompts with ${{ binding }} interpolation and delegates the
// actual model call to an injected RunAgent function — pantheon's
// agent.Agent.Run fits that shape, so hosts wire it with one closure.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	skills "github.com/odysseythink/pantheon/skills-engine"
)

// BatchLimit caps pipeline fan-out and parallel fan-in so a runaway plan
// cannot flood the provider.
const BatchLimit = 32

// RunAgentFunc runs one prompt through an agent and returns its text
// output. agentName is informational (progress reporting, logging).
type RunAgentFunc func(ctx context.Context, prompt string) (string, error)

// Step status values for StepEvent.
const (
	StepStarted  = "started"
	StepFinished = "finished"
)

// StepEvent reports executor progress; hosts typically forward these to a
// UI as wire frames.
type StepEvent struct {
	PhaseID string
	Index   int
	Kind    string // "agent" | "pipeline" | "parallel"
	Status  string // StepStarted | StepFinished
	Detail  string // e.g. the item being processed in a pipeline
}

// Result is the outcome of a completed flow run.
type Result struct {
	// Outputs holds every step output bound with `output:`.
	Outputs map[string]any
	// Final is the last step's value rendered as text (JSON for arrays).
	Final string
}

// Executor runs FlowPlans. RunAgent is required; OnStep is optional.
type Executor struct {
	RunAgent RunAgentFunc
	OnStep   func(StepEvent)
}

var interpolationRe = regexp.MustCompile(`\$\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Run executes the plan with input bound as ${{ input }}.
func (e *Executor) Run(ctx context.Context, plan *skills.FlowPlan, input string) (*Result, error) {
	if e == nil || e.RunAgent == nil {
		return nil, fmt.Errorf("flow: Executor.RunAgent is required")
	}
	bindings := map[string]any{"input": input}
	res := &Result{Outputs: map[string]any{}}
	var last any
	for _, phase := range plan.Phases {
		for i, step := range phase.Steps {
			value, err := e.runStep(ctx, phase.ID, i, step, bindings)
			if err != nil {
				return nil, fmt.Errorf("flow: phase %q step %d: %w", phase.ID, i, err)
			}
			if step.Output != "" {
				bindings[step.Output] = value
				res.Outputs[step.Output] = value
			}
			if value != nil {
				last = value
			}
		}
	}
	res.Final = renderValue(last)
	return res, nil
}

func (e *Executor) emit(phaseID string, index int, kind, status, detail string) {
	if e.OnStep != nil {
		e.OnStep(StepEvent{PhaseID: phaseID, Index: index, Kind: kind, Status: status, Detail: detail})
	}
}

// runStep executes one step against bindings, returning its value.
func (e *Executor) runStep(ctx context.Context, phaseID string, index int, step skills.FlowStep, bindings map[string]any) (any, error) {
	kind := step.Kind()
	e.emit(phaseID, index, kind, StepStarted, "")
	value, err := e.dispatch(ctx, phaseID, index, step, bindings)
	if err != nil {
		return nil, err
	}
	e.emit(phaseID, index, kind, StepFinished, "")
	return value, nil
}

func (e *Executor) dispatch(ctx context.Context, phaseID string, index int, step skills.FlowStep, bindings map[string]any) (any, error) {
	switch step.Kind() {
	case "agent":
		prompt, err := render(step.Agent, bindings, nil)
		if err != nil {
			return nil, err
		}
		return e.RunAgent(ctx, prompt)

	case "pipeline":
		items, err := evalItems(step.Pipeline, bindings)
		if err != nil {
			return nil, err
		}
		if len(items) > BatchLimit {
			return nil, fmt.Errorf("pipeline produced %d items, batch limit is %d", len(items), BatchLimit)
		}
		results := make([]string, 0, len(items))
		for _, item := range items {
			prompt, err := render(step.Each, bindings, map[string]any{"item": item})
			if err != nil {
				return nil, err
			}
			e.emit(phaseID, index, "pipeline", StepStarted, item)
			out, err := e.RunAgent(ctx, prompt)
			if err != nil {
				return nil, fmt.Errorf("pipeline item %q: %w", item, err)
			}
			e.emit(phaseID, index, "pipeline", StepFinished, item)
			results = append(results, out)
		}
		return results, nil

	case "parallel":
		if len(step.Parallel) > BatchLimit {
			return nil, fmt.Errorf("parallel block has %d steps, batch limit is %d", len(step.Parallel), BatchLimit)
		}
		return e.runParallel(ctx, phaseID, step, bindings)
	}
	return nil, fmt.Errorf("step must declare one of agent/pipeline/parallel")
}

// runParallel runs sub-steps concurrently against a pre-start snapshot of
// the bindings; their outputs merge back only after all of them succeed.
func (e *Executor) runParallel(ctx context.Context, phaseID string, step skills.FlowStep, bindings map[string]any) (any, error) {
	snapshot := cloneBindings(bindings)

	type childResult struct {
		updates map[string]any
		value   any
	}
	results := make([]childResult, len(step.Parallel))
	errs := make([]error, len(step.Parallel))

	var wg sync.WaitGroup
	for i, sub := range step.Parallel {
		wg.Add(1)
		go func(i int, sub skills.FlowStep) {
			defer wg.Done()
			childBindings := cloneBindings(snapshot)
			value, err := e.runStep(ctx, phaseID, i, sub, childBindings)
			if err != nil {
				errs[i] = err
				return
			}
			if sub.Output != "" {
				results[i].updates = map[string]any{sub.Output: value}
			}
			results[i].value = value
		}(i, sub)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("parallel[%d]: %w", i, err)
		}
	}
	for _, r := range results {
		for k, v := range r.updates {
			bindings[k] = v
		}
	}
	return nil, nil
}

// render substitutes ${{ name }} references in tmpl. extra carries
// step-local bindings (e.g. item in a pipeline) and shadows bindings.
func render(tmpl string, bindings, extra map[string]any) (string, error) {
	var missing string
	out := interpolationRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		name := interpolationRe.FindStringSubmatch(match)[1]
		value, ok := extra[name]
		if !ok {
			value, ok = bindings[name]
		}
		if !ok {
			missing = name
			return match
		}
		return renderValue(value)
	})
	if missing != "" {
		return "", fmt.Errorf("undefined binding %q", missing)
	}
	return out, nil
}

// evalItems resolves a pipeline expression to its item list. A whole
// ${{ binding }} reference resolves the binding directly; arrays pass
// through, strings parse as a JSON array and fall back to non-empty lines.
func evalItems(expr string, bindings map[string]any) ([]string, error) {
	trimmed := strings.TrimSpace(expr)
	if m := interpolationRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
		value, ok := bindings[m[1]]
		if !ok {
			return nil, fmt.Errorf("undefined binding %q", m[1])
		}
		switch v := value.(type) {
		case []string:
			return v, nil
		case string:
			return splitItems(v), nil
		}
		return nil, fmt.Errorf("binding %q is not a list", m[1])
	}
	rendered, err := render(expr, bindings, nil)
	if err != nil {
		return nil, err
	}
	return splitItems(rendered), nil
}

func splitItems(s string) []string {
	var arr []string
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &arr) == nil {
		return arr
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// renderValue renders a binding as prompt text: strings verbatim, arrays
// as compact JSON.
func renderValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(raw)
	}
}

func cloneBindings(bindings map[string]any) map[string]any {
	out := make(map[string]any, len(bindings))
	for k, v := range bindings {
		out[k] = v
	}
	return out
}
