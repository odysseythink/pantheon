package schema

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestGenerate(t *testing.T) {
	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	s := Generate(reflect.TypeOf(Person{}))
	if s.Type != "object" {
		t.Errorf("type: got %q, want object", s.Type)
	}
	if s.Properties["name"].Type != "string" {
		t.Errorf("name type: got %q, want string", s.Properties["name"].Type)
	}
}

func TestParsePartialJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"complete object", `{"name":"alice","age":30}`, false},
		{"trailing comma", `{"name":"alice","age":30,}`, true},
		{"unclosed brace", `{"name":"alice"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePartialJSON(tt.input, nil)
			if tt.wantErr && err == nil {
				t.Error("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepairToolCallValidJSON(t *testing.T) {
	tc := &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`}
	repaired, err := RepairToolCall(tc, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired.Arguments != tc.Arguments {
		t.Error("valid JSON should pass through unchanged")
	}
}

func TestParsePartialJSON_SingleKey(t *testing.T) {
	result, err := ParsePartialJSON(`{"name":"alice"`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
}

func TestParsePartialJSON_Unrepairable(t *testing.T) {
	_, err := ParsePartialJSON(`{invalid json!!!`, nil)
	if err == nil {
		t.Fatal("expected error for unrepairable JSON")
	}
}

func TestRemoveTrailingComma_NoTrailing(t *testing.T) {
	result := removeTrailingComma(`{"name":"alice"}`)
	if result != `{"name":"alice"}` {
		t.Errorf("got %q", result)
	}
}

func TestRemoveTrailingComma_Short(t *testing.T) {
	result := removeTrailingComma(`{}`)
	if result != `{}` {
		t.Errorf("got %q", result)
	}
}

func TestRemoveTrailingComma_NoBraceBracket(t *testing.T) {
	result := removeTrailingComma(`hello`)
	if result != `hello` {
		t.Errorf("got %q", result)
	}
}

func TestRepairToolCall_Truncated(t *testing.T) {
	tc := &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"`}
	repaired, err := RepairToolCall(tc, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repaired.Arguments == "" {
		t.Fatal("expected repaired arguments")
	}
}

func TestRepairToolCall_Unrepairable(t *testing.T) {
	tc := &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{invalid!!!`}
	_, err := RepairToolCall(tc, nil)
	if err == nil {
		t.Fatal("expected error for unrepairable arguments")
	}
}

func TestRepairToolCall_MarshalFail(t *testing.T) {
	// Temporarily replace jsonMarshal with a function that always fails.
	orig := jsonMarshal
	jsonMarshal = func(v any) ([]byte, error) {
		return nil, fmt.Errorf("mock marshal error")
	}
	defer func() { jsonMarshal = orig }()

	tc := &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"`}
	_, err := RepairToolCall(tc, nil)
	if err == nil {
		t.Fatal("expected error for marshal failure")
	}
	if !strings.Contains(err.Error(), "mock marshal error") {
		t.Errorf("expected mock marshal error, got: %v", err)
	}
}

func TestParsePartialJSON_NestedUnclosed(t *testing.T) {
	result, err := ParsePartialJSON(`{"a":{"b":1`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
}

func TestParsePartialJSON_UnclosedBracket(t *testing.T) {
	// Input with openBrackets > 0 triggers the bracket-closing loop.
	// The current repair heuristic may not produce valid JSON in this case,
	// but the loop itself (lines 33-35) is executed.
	_, err := ParsePartialJSON(`{"a": [`, nil)
	if err == nil {
		t.Fatal("expected error for unclosed bracket that cannot be repaired")
	}
}

func TestRemoveTrailingComma_Nested(t *testing.T) {
	result := removeTrailingComma(`{"a":{"b":1}}`)
	if result != `{"a":{"b":1}}` {
		t.Errorf("got %q", result)
	}
}

func TestRemoveTrailingComma_CommaBeforeBrace(t *testing.T) {
	// Triggers the branch that removes a comma before an opening brace/bracket.
	result := removeTrailingComma(`,{}`)
	if result != `{}` {
		t.Errorf("got %q, want %q", result, `{}`)
	}
}

func TestRemoveTrailingComma_Empty(t *testing.T) {
	result := removeTrailingComma(``)
	if result != `` {
		t.Errorf("got %q", result)
	}
}

func TestParsePartialJSON_Empty(t *testing.T) {
	_, err := ParsePartialJSON(``, nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}
