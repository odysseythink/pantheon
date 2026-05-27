package agent

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestStepCountIs(t *testing.T) {
	cond := StepCountIs(3)

	// step 0, 1, 2 should not stop
	for i := 0; i < 3; i++ {
		if cond(i, nil, nil) {
			t.Errorf("StepCountIs(3): should not stop at step %d", i)
		}
	}

	// step 3 and beyond should stop
	for i := 3; i < 6; i++ {
		if !cond(i, nil, nil) {
			t.Errorf("StepCountIs(3): should stop at step %d", i)
		}
	}
}

func TestHasToolCall(t *testing.T) {
	cond := HasToolCall("finish")

	// No tool calls
	resp := &core.Response{Message: core.Message{Content: []core.ContentParter{core.TextPart{Text: "hello"}}}}
	if cond(0, resp, nil) {
		t.Error("HasToolCall: should not stop when no tool calls")
	}

	// Different tool call
	resp = &core.Response{Message: core.Message{Content: []core.ContentParter{core.ToolCallPart{Name: "other"}}}}
	if cond(0, resp, nil) {
		t.Error("HasToolCall: should not stop for different tool")
	}

	// Matching tool call
	resp = &core.Response{Message: core.Message{Content: []core.ContentParter{core.ToolCallPart{Name: "finish"}}}}
	if !cond(0, resp, nil) {
		t.Error("HasToolCall: should stop for matching tool")
	}
}

func TestFinishReasonIs(t *testing.T) {
	cond := FinishReasonIs("stop")

	if cond(0, nil, nil) {
		t.Error("FinishReasonIs: should not stop on nil resp")
	}

	resp := &core.Response{FinishReason: "length"}
	if cond(0, resp, nil) {
		t.Error("FinishReasonIs: should not stop for different reason")
	}

	resp = &core.Response{FinishReason: "stop"}
	if !cond(0, resp, nil) {
		t.Error("FinishReasonIs: should stop for matching reason")
	}
}

func TestMaxTokensUsed(t *testing.T) {
	cond := MaxTokensUsed(100)

	if cond(0, nil, nil) {
		t.Error("MaxTokensUsed: should not stop on nil resp")
	}

	resp := &core.Response{Usage: core.Usage{TotalTokens: 50}}
	if cond(0, resp, nil) {
		t.Error("MaxTokensUsed: should not stop below limit")
	}

	resp = &core.Response{Usage: core.Usage{TotalTokens: 100}}
	if !cond(0, resp, nil) {
		t.Error("MaxTokensUsed: should stop at limit")
	}

	resp = &core.Response{Usage: core.Usage{TotalTokens: 150}}
	if !cond(0, resp, nil) {
		t.Error("MaxTokensUsed: should stop above limit")
	}
}

func TestAnyOf(t *testing.T) {
	cond := AnyOf(
		StepCountIs(2),
		FinishReasonIs("stop"),
	)

	// Neither met
	if cond(0, &core.Response{FinishReason: "length"}, nil) {
		t.Error("AnyOf: should not stop when neither condition is met")
	}

	// First met
	if !cond(2, &core.Response{FinishReason: "length"}, nil) {
		t.Error("AnyOf: should stop when first condition is met")
	}

	// Second met
	if !cond(0, &core.Response{FinishReason: "stop"}, nil) {
		t.Error("AnyOf: should stop when second condition is met")
	}
}

func TestAllOf(t *testing.T) {
	cond := AllOf(
		StepCountIs(2),
		FinishReasonIs("stop"),
	)

	// Only first met
	if cond(2, &core.Response{FinishReason: "length"}, nil) {
		t.Error("AllOf: should not stop when only first condition is met")
	}

	// Only second met
	if cond(0, &core.Response{FinishReason: "stop"}, nil) {
		t.Error("AllOf: should not stop when only second condition is met")
	}

	// Both met
	if !cond(2, &core.Response{FinishReason: "stop"}, nil) {
		t.Error("AllOf: should stop when all conditions are met")
	}
}

func TestAllOf_Empty(t *testing.T) {
	cond := AllOf()
	if cond(0, nil, nil) {
		t.Error("AllOf with no conditions should not stop")
	}
}
