package budget

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBudgetConsume(t *testing.T) {
	b := New(3)
	assert.Equal(t, 3, b.Remaining())
	assert.True(t, b.Consume())
	assert.Equal(t, 2, b.Remaining())
	assert.True(t, b.Consume())
	assert.True(t, b.Consume())
	assert.False(t, b.Consume(), "budget exhausted")
	assert.Equal(t, -1, b.Remaining())
}

func TestBudgetRefund(t *testing.T) {
	b := New(2)
	b.Consume()
	b.Consume()
	assert.Equal(t, 0, b.Remaining())
	b.Refund()
	assert.Equal(t, 1, b.Remaining())
}

func TestBudgetRatio(t *testing.T) {
	b := New(10)
	assert.Equal(t, 0.0, b.Ratio())
	for i := 0; i < 7; i++ {
		b.Consume()
	}
	assert.InDelta(t, 0.7, b.Ratio(), 0.01)
}

func TestBudgetConcurrentConsume(t *testing.T) {
	b := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				b.Consume()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 0, b.Remaining())
}
