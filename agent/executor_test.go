package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecuteToolSuccess(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) {
		return "result: " + args, nil
	}

	res, err := executeTool(context.Background(), "test", "hello", fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "result: hello" {
		t.Errorf("result: got %q, want 'result: hello'", res)
	}
}

func TestExecuteToolError(t *testing.T) {
	expectedErr := errors.New("something went wrong")
	fn := func(ctx context.Context, args string) (string, error) {
		return "", expectedErr
	}

	res, err := executeTool(context.Background(), "test", "", fn)
	if err == nil {
		t.Fatal("expected error")
	}
	if res != "" {
		t.Errorf("result: got %q, want empty", res)
	}
	if !errors.Is(err, expectedErr) {
		t.Errorf("error: got %v, want %v", err, expectedErr)
	}
}

func TestExecuteToolPanicRecovery(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) {
		panic("intentional panic")
	}

	res, err := executeTool(context.Background(), "test", "", fn)
	if err == nil {
		t.Fatal("expected error after panic")
	}
	if res != "" {
		t.Errorf("result: got %q, want empty", res)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error: got %q, want to contain 'panicked'", err.Error())
	}
	if !strings.Contains(err.Error(), "intentional panic") {
		t.Errorf("error: got %q, want to contain 'intentional panic'", err.Error())
	}
}

func TestExecuteToolTimeout(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "too late", nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res, err := executeTool(ctx, "slow", "", fn)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if res != "" {
		t.Errorf("result: got %q, want empty", res)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error: got %q, want to contain 'timed out'", err.Error())
	}
}

func TestExecuteToolRespectsContextCancellation(t *testing.T) {
	fn := func(ctx context.Context, args string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "too late", nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	res, err := executeTool(ctx, "cancelable", "", fn)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if res != "" {
		t.Errorf("result: got %q, want empty", res)
	}
	if !strings.Contains(err.Error(), "timed out") && !errors.Is(err, context.Canceled) {
		t.Errorf("error: got %v, want timeout or canceled", err)
	}
}
