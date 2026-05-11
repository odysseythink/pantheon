package schema

import (
	"reflect"
	"testing"

	"github.com/odysseythink/ai/core"
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
