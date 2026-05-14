// Package budget provides an atomic iteration counter for agent loops.
package budget

import "sync/atomic"

// Budget tracks the remaining iteration budget for a conversation.
// Thread-safe via atomic.Int32. The zero value is not valid; use New.
type Budget struct {
	max       int
	remaining atomic.Int32
}

// New constructs a Budget with max iterations.
func New(max int) *Budget {
	b := &Budget{max: max}
	b.remaining.Store(int32(max))
	return b
}

// Consume attempts to use one iteration. Returns true if the budget was
// decremented while non-negative, false if it went negative.
func (b *Budget) Consume() bool {
	return b.remaining.Add(-1) >= 0
}

// Refund returns one iteration to the budget.
func (b *Budget) Refund() {
	b.remaining.Add(1)
}

// Remaining returns the current remaining iteration count.
func (b *Budget) Remaining() int {
	return int(b.remaining.Load())
}

// Ratio returns the fraction consumed, from 0.0 (fresh) to 1.0 (exhausted).
func (b *Budget) Ratio() float64 {
	if b.max == 0 {
		return 0
	}
	used := b.max - int(b.remaining.Load())
	return float64(used) / float64(b.max)
}
