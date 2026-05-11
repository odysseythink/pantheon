package agent

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
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
