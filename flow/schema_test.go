package flow

import (
	"strings"
	"testing"
)

// Mirrors ody-rs core/src/flow/schema.rs tests: the same JSON-schema subset
// (type/properties/required/items/enum), the same malformed-schema errors,
// and the same "unknown keywords are ignored" forward compatibility.

func TestSchemaAcceptsMatchingObject(t *testing.T) {
	schema := map[string]any{
		"type":     "object",
		"required": []any{"mechanics"},
		"properties": map[string]any{
			"mechanics": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":       "object",
					"required":   []any{"name"},
					"properties": map[string]any{"name": map[string]any{"type": "string"}},
				},
			},
		},
	}
	value := map[string]any{"mechanics": []any{map[string]any{"name": "jump"}}, "extra": 1}
	if err := validateAgainstSchema(value, schema); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestSchemaRejectsWrongTypeWithPath(t *testing.T) {
	err := validateAgainstSchema([]any{}, map[string]any{"type": "object"})
	if err == nil || !strings.Contains(err.Error(), "expected object at $") {
		t.Fatalf("err = %v, want expected-object-at-$", err)
	}
}

func TestSchemaRejectsMissingRequiredProperty(t *testing.T) {
	err := validateAgainstSchema(map[string]any{}, map[string]any{"type": "object", "required": []any{"name"}})
	if err == nil || !strings.Contains(err.Error(), "missing required property 'name'") {
		t.Fatalf("err = %v, want missing-required", err)
	}
}

func TestSchemaRejectsNestedPropertyMismatch(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"gdd": map[string]any{"type": "object", "required": []any{"constraints"}},
		},
	}
	err := validateAgainstSchema(map[string]any{"gdd": map[string]any{}}, schema)
	if err == nil || !strings.Contains(err.Error(), "$.gdd") {
		t.Fatalf("err = %v, want nested path $.gdd", err)
	}
}

func TestSchemaIntegerAcceptsIntsAndRejectsFloats(t *testing.T) {
	schema := map[string]any{"type": "integer"}
	if err := validateAgainstSchema(float64(3), schema); err != nil {
		t.Fatalf("int: %v", err)
	}
	if err := validateAgainstSchema(3.5, schema); err == nil || !strings.Contains(err.Error(), "expected integer") {
		t.Fatalf("float err = %v, want expected-integer", err)
	}
}

func TestSchemaNumberAcceptsIntsAndFloats(t *testing.T) {
	schema := map[string]any{"type": "number"}
	if err := validateAgainstSchema(float64(3), schema); err != nil {
		t.Fatalf("int: %v", err)
	}
	if err := validateAgainstSchema(3.5, schema); err != nil {
		t.Fatalf("float: %v", err)
	}
}

func TestSchemaEnumRejectsOutsideValues(t *testing.T) {
	schema := map[string]any{"enum": []any{"a", "b"}}
	if err := validateAgainstSchema("a", schema); err != nil {
		t.Fatalf("allowed: %v", err)
	}
	if err := validateAgainstSchema("c", schema); err == nil {
		t.Fatal("c must be rejected")
	}
}

func TestSchemaMalformedIsAnError(t *testing.T) {
	if err := validateAgainstSchema(nil, map[string]any{"type": float64(3)}); err == nil {
		t.Fatal("non-string type keyword must error")
	}
	if err := validateAgainstSchema(map[string]any{}, map[string]any{"required": "name"}); err == nil {
		t.Fatal("non-array required keyword must error")
	}
}

func TestSchemaUnknownKeywordsAreIgnored(t *testing.T) {
	schema := map[string]any{"description": "free-form", "minimum": float64(3)}
	if err := validateAgainstSchema(float64(1), schema); err != nil {
		t.Fatalf("unknown keywords must be ignored: %v", err)
	}
}
