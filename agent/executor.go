package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/odysseythink/pantheon/core"
	"golang.org/x/sync/errgroup"
)

// ToolFunc is the signature for executable tools.
type ToolFunc func(ctx context.Context, args string) (string, error)

// executeTool runs a tool with panic recovery and timeout.
func executeTool(ctx context.Context, name string, args string, fn ToolFunc) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resultCh := make(chan struct {
		value string
		err   error
	}, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- struct {
					value string
					err   error
				}{"", fmt.Errorf("tool %q panicked: %v\n%s", name, r, debug.Stack())}
			}
		}()
		val, err := fn(ctx, args)
		resultCh <- struct {
			value string
			err   error
		}{val, err}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("tool %q timed out: %w", name, ctx.Err())
	case res := <-resultCh:
		return res.value, res.err
	}
}


// toolCallResult holds the outcome of a single tool execution.
type toolCallResult struct {
	toolCallID string
	name       string
	result     string
	isError    bool
	stopTurn   bool
}

// executeToolCalls runs a slice of tool calls. Tools whose names appear in
// parallelMap are executed concurrently within their contiguous block;
// all other tools run sequentially. The returned slice preserves the
// original order of calls.
func executeToolCalls(
	ctx context.Context,
	calls []core.ToolCallPart,
	parallelMap map[string]bool,
	execute func(ctx context.Context, tc core.ToolCallPart) (core.ToolResponse, error),
) ([]toolCallResult, error) {
	results := make([]toolCallResult, len(calls))

	i := 0
	for i < len(calls) {
		if parallelMap[calls[i].Name] {
			// Find the contiguous block of parallel calls.
			j := i
			for j < len(calls) && parallelMap[calls[j].Name] {
				j++
			}
			block := calls[i:j]
			blockResults := make([]toolCallResult, len(block))
			var g errgroup.Group
			var mu sync.Mutex
			for k, tc := range block {
				k, tc := k, tc // capture loop vars
				g.Go(func() error {
					resp, err := execute(ctx, tc)
					mu.Lock()
					if err != nil {
						blockResults[k] = toolCallResult{toolCallID: tc.ID, name: tc.Name, result: err.Error(), isError: true}
					} else {
						blockResults[k] = toolCallResult{toolCallID: tc.ID, name: tc.Name, result: resp.Content, isError: resp.IsError, stopTurn: resp.StopTurn}
					}
					mu.Unlock()
					return nil
				})
			}
			if err := g.Wait(); err != nil {
				return nil, err
			}
			copy(results[i:], blockResults)
			i = j
		} else {
			// Sequential execution.
			resp, err := execute(ctx, calls[i])
			if err != nil {
				results[i] = toolCallResult{toolCallID: calls[i].ID, name: calls[i].Name, result: err.Error(), isError: true}
			} else {
				results[i] = toolCallResult{toolCallID: calls[i].ID, name: calls[i].Name, result: resp.Content, isError: resp.IsError, stopTurn: resp.StopTurn}
			}
			i++
		}
	}

	return results, nil
}
