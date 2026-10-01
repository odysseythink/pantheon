package memory

import (
	"fmt"
	"time"
)

// Latency timeout primitives (D43-A — 100ms soft / 200ms hard).
//
// The recall pipeline is on the user's per-turn critical path. Default
// budgets: atom FTS ≤ 50ms, page lookup ≤ 30ms, raw FTS ≤ 30ms, total
// recall ≤ 200ms (hard kill — return whatever's accumulated).
//
// Go translation of Python's thread-pool future.result(timeout): run fn
// in a goroutine and abandon it on timeout. The abandoned goroutine is
// allowed to finish in the background (we can't kill a native SQLite call
// cleanly), but we don't wait for it — callers MUST tolerate the duplicate
// work (idempotent reads only), matching the Python contract.

const (
	DefaultTotalBudgetMS = 200
	DefaultAtomBudgetMS  = 50
	DefaultPageBudgetMS  = 30
	DefaultRawBudgetMS   = 30
)

// TimeoutExceededError is returned when one recall stage exceeds its time
// budget. The caller typically catches it and skips to the next source
// rather than propagating it to the user.
type TimeoutExceededError struct {
	Stage    string
	BudgetMS int
}

func (e *TimeoutExceededError) Error() string {
	return fmt.Sprintf("memory: stage %q exceeded %dms budget", e.Stage, e.BudgetMS)
}

// WithDeadline runs fn and aborts with *TimeoutExceededError if it doesn't
// return within budgetMS.
func WithDeadline[T any](stage string, budgetMS int, fn func() (T, error)) (T, error) {
	var zero T
	if budgetMS <= 0 {
		// Caller has already burned the budget — short-circuit.
		return zero, &TimeoutExceededError{Stage: stage, BudgetMS: budgetMS}
	}
	type outcome struct {
		val T
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		v, err := fn()
		ch <- outcome{v, err}
	}()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-time.After(time.Duration(budgetMS) * time.Millisecond):
		return zero, &TimeoutExceededError{Stage: stage, BudgetMS: budgetMS}
	}
}

// Stopwatch tracks elapsed time so each stage knows the remaining global
// budget. Usage:
//
//	sw := NewStopwatch(200)
//	atoms, err := WithDeadline("atom", min(50, sw.RemainingMS()), fetchAtoms)
//	sw.Split("atom")
type Stopwatch struct {
	totalBudgetMS int
	startedAt     time.Time
	Splits        map[string]float64 // stage -> elapsed ms at split time
}

// NewStopwatch starts a stopwatch with the given total budget in ms.
func NewStopwatch(totalBudgetMS int) *Stopwatch {
	return &Stopwatch{
		totalBudgetMS: totalBudgetMS,
		startedAt:     time.Now(),
		Splits:        map[string]float64{},
	}
}

// ElapsedMS returns elapsed milliseconds since start.
func (sw *Stopwatch) ElapsedMS() int {
	return int(time.Since(sw.startedAt).Milliseconds())
}

// RemainingMS returns the remaining global budget (never negative).
func (sw *Stopwatch) RemainingMS() int {
	r := sw.totalBudgetMS - sw.ElapsedMS()
	if r < 0 {
		return 0
	}
	return r
}

// Expired reports whether the global budget is exhausted.
func (sw *Stopwatch) Expired() bool { return sw.RemainingMS() <= 0 }

// Split records a stage boundary for diagnostics.
func (sw *Stopwatch) Split(label string) {
	sw.Splits[label] = float64(time.Since(sw.startedAt).Milliseconds())
}
