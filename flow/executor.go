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

// SchemaMaxAttempts bounds schema-constrained agents (ody-rs M2.4): the
// initial try plus retries after parse/validation failures, each retry
// carrying the previous failure reason in its prompt.
const SchemaMaxAttempts = 3

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

var interpolationRe = regexp.MustCompile(`\$\{\{\s*([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*)\s*\}\}`)

// itemRefRe is the single-brace form ${item} / ${item.path} — resolved only
// inside a pipeline each template (ody-rs interp.rs); elsewhere single-brace
// text stays literal so shell-style ${VAR} in prompts is untouched.
var itemRefRe = regexp.MustCompile(`\$\{\s*(item(?:\.[A-Za-z0-9_]+)*)\s*\}`)

// Run executes the plan with input bound as ${{ input }}.
func (e *Executor) Run(ctx context.Context, plan *skills.FlowPlan, input string) (*Result, error) {
	return e.RunWithArgs(ctx, plan, input, nil)
}

// RunWithArgs is Run plus structured trigger inputs (ody-rs args root): the
// args map is pre-bound so templates can address ${{ args.key }} with full
// path support. A nil map binds an empty object.
func (e *Executor) RunWithArgs(ctx context.Context, plan *skills.FlowPlan, input string, args map[string]any) (*Result, error) {
	if e == nil || e.RunAgent == nil {
		return nil, fmt.Errorf("flow: Executor.RunAgent is required")
	}
	if args == nil {
		args = map[string]any{}
	}
	bindings := map[string]any{"input": input, "args": args}
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
		raw, err := e.runAgentText(ctx, prompt, step.Schema)
		if err != nil {
			return nil, err
		}
		return parseAgentOutput(raw), nil

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
			detail := renderValue(item)
			e.emit(phaseID, index, "pipeline", StepStarted, detail)
			out, err := e.RunAgent(ctx, prompt)
			if err != nil {
				return nil, fmt.Errorf("pipeline item %q: %w", detail, err)
			}
			e.emit(phaseID, index, "pipeline", StepFinished, detail)
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

// render substitutes ${{ name }} / ${{ name.path }} references in tmpl.
// extra carries step-local bindings (e.g. item in a pipeline) and shadows
// bindings; inside a pipeline each template the single-brace ${item} /
// ${item.path} form resolves against the current item as well.
func render(tmpl string, bindings, extra map[string]any) (string, error) {
	var rerr error
	out := interpolationRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		value, err := resolveExpr(interpolationRe.FindStringSubmatch(match)[1], bindings, extra)
		if err != nil {
			rerr = err
			return match
		}
		return renderValue(value)
	})
	if rerr != nil {
		return "", rerr
	}
	item, ok := extra["item"]
	if !ok {
		return out, nil
	}
	out = itemRefRe.ReplaceAllStringFunc(out, func(match string) string {
		segments := strings.Split(itemRefRe.FindStringSubmatch(match)[1], ".")
		value, err := resolvePath(item, segments[1:])
		if err != nil {
			rerr = err
			return match
		}
		return renderValue(value)
	})
	if rerr != nil {
		return "", rerr
	}
	return out, nil
}

// resolveExpr resolves a dotted ${{ }} expression: the first segment names
// the binding (extra shadows bindings, so the pipeline item wins), the rest
// walk into structured values.
func resolveExpr(expr string, bindings, extra map[string]any) (any, error) {
	segments := strings.Split(expr, ".")
	root, ok := extra[segments[0]]
	if !ok {
		root, ok = bindings[segments[0]]
	}
	if !ok {
		return nil, fmt.Errorf("undefined binding %q", segments[0])
	}
	value, err := resolvePath(root, segments[1:])
	if err != nil {
		return nil, fmt.Errorf("binding %q: %w", segments[0], err)
	}
	return value, nil
}

// resolvePath walks dot-separated segments: object keys on maps, numeric
// indices on arrays (ody-rs parse_path allows digits in any segment).
func resolvePath(root any, segments []string) (any, error) {
	current := root
	for _, seg := range segments {
		switch c := current.(type) {
		case map[string]any:
			v, ok := c[seg]
			if !ok {
				return nil, fmt.Errorf("missing path %q", seg)
			}
			current = v
		case []any:
			v, err := arrayIndex(c, seg)
			if err != nil {
				return nil, err
			}
			current = v
		case []string:
			v, err := arrayIndex(c, seg)
			if err != nil {
				return nil, err
			}
			current = v
		default:
			return nil, fmt.Errorf("missing path %q", seg)
		}
	}
	return current, nil
}

func arrayIndex[S ~[]E, E any](arr S, seg string) (E, error) {
	var zero E
	idx := 0
	for _, r := range seg {
		if r < '0' || r > '9' {
			return zero, fmt.Errorf("missing path %q", seg)
		}
		idx = idx*10 + int(r-'0')
	}
	if idx >= len(arr) {
		return zero, fmt.Errorf("missing path %q", seg)
	}
	return arr[idx], nil
}

// evalItems resolves a pipeline expression to its item list. A whole
// ${{ binding }} reference resolves the binding directly (paths included);
// arrays pass through keeping element structure (so ${{ item.x }} works),
// strings parse as a JSON array and fall back to non-empty lines.
func evalItems(expr string, bindings map[string]any) ([]any, error) {
	trimmed := strings.TrimSpace(expr)
	if m := interpolationRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
		value, err := resolveExpr(m[1], bindings, nil)
		if err != nil {
			return nil, err
		}
		return itemsOf(value, m[1])
	}
	rendered, err := render(expr, bindings, nil)
	if err != nil {
		return nil, err
	}
	return splitItems(rendered), nil
}

func splitItems(s string) []any {
	var arr []any
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &arr) == nil && arr != nil {
		return arr
	}
	var lines []any
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// runAgentText runs one agent prompt, applying M2.4 schema semantics: with
// a schema the JSON requirements are injected at the prompt tail and each
// validation failure feeds its reason back into the retry prompt. Without a
// schema the loop runs exactly once and behaves like the pre-schema path.
func (e *Executor) runAgentText(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	if schema != nil {
		prompt += schemaPromptSuffix(schema)
	}
	var lastReason string
	for attempt := 0; attempt < SchemaMaxAttempts; attempt++ {
		attemptPrompt := prompt
		if lastReason != "" {
			attemptPrompt = prompt + "\n\nYour previous reply failed validation: " + lastReason + "\nReply again with corrected JSON only."
		}
		raw, err := e.RunAgent(ctx, attemptPrompt)
		if err != nil {
			return "", err
		}
		if schema == nil {
			return raw, nil
		}
		if _, verr := validateAgentOutput(raw, schema); verr == nil {
			return raw, nil
		} else {
			lastReason = verr.Error()
		}
	}
	return "", fmt.Errorf("schema validation failed after %d attempts: %s", SchemaMaxAttempts, lastReason)
}

// validateAgentOutput requires a schema-constrained reply to be valid JSON
// satisfying the schema subset, and returns the parsed value.
func validateAgentOutput(raw string, schema map[string]any) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &value); err != nil {
		return nil, fmt.Errorf("reply is not valid JSON: %v", err)
	}
	if err := validateAgainstSchema(value, schema); err != nil {
		return nil, err
	}
	return value, nil
}

// parseAgentOutput stores agent results as parsed JSON when possible, else
// as the raw string (ody-rs parse_agent_output) — structured values are what
// makes ${{ name.path }} interpolation useful across steps.
func parseAgentOutput(raw string) any {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err == nil {
		return value
	}
	return raw
}

// itemsOf normalizes a pipeline source binding into the item list: arrays
// pass through keeping element structure (a JSON object item stays an
// object, so ${{ item.name }} resolves), strings parse as a JSON array and
// fall back to non-empty lines.
func itemsOf(value any, name string) ([]any, error) {
	switch v := value.(type) {
	case []any:
		return v, nil
	case []string:
		items := make([]any, len(v))
		for i, element := range v {
			items[i] = element
		}
		return items, nil
	case string:
		return splitItems(v), nil
	}
	return nil, fmt.Errorf("binding %q is not a list", name)
}

// prettyJSON renders a value as indented JSON for prompt injection.
func prettyJSON(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
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
