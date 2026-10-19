// schema.go — minimal JSON-schema subset validator for flow agent `schema:`
// fields (port of ody-rs core/src/flow/schema.rs, M2.4). There is no
// jsonschema dependency here by design: the validator implements exactly the
// subset flow plans need and ignores unknown keywords (forward-compatible
// with richer schemas).
//
// Supported keywords:
//   - type: object | array | string | number | integer | boolean | null
//   - properties: { name: <schema> } (objects)
//   - required: [name, ...] (objects)
//   - items: <schema> (arrays)
//   - enum: [value, ...] (any type)
//
// Anything else is ignored. A malformed schema (e.g. a non-string `type`)
// is an error, surfaced as the validation failure reason so retry feedback
// is actionable.
package flow

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

// validateAgainstSchema validates value (encoding/json decoded: map, slice,
// string, float64, bool, nil) against the schema subset, returning a
// human-readable reason for the first mismatch.
func validateAgainstSchema(value any, schema map[string]any) error {
	return validateSchemaInner(value, schema, "$")
}

func validateSchemaInner(value any, schema map[string]any, path string) error {
	if expected, ok := schema["type"]; ok {
		expectedStr, ok := expected.(string)
		if !ok {
			return fmt.Errorf("schema keyword 'type' must be a string")
		}
		if actual := jsonTypeOf(value); !typeMatches(actual, expectedStr) {
			return fmt.Errorf("expected %s at %s, found %s", expectedStr, path, actual)
		}
	}

	if variants, ok := schema["enum"]; ok {
		list, ok := variants.([]any)
		if !ok {
			return fmt.Errorf("schema keyword 'enum' must be an array")
		}
		found := false
		for _, variant := range list {
			if reflect.DeepEqual(variant, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("value at %s is not one of the allowed values", path)
		}
	}

	if properties, ok := schema["properties"]; ok {
		props, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("schema keyword 'properties' must be an object")
		}
		if object, ok := value.(map[string]any); ok {
			for name, propertySchema := range props {
				propertyValue, ok := object[name]
				if !ok {
					continue
				}
				sub, ok := propertySchema.(map[string]any)
				if !ok {
					return fmt.Errorf("schema for property '%s' must be an object", name)
				}
				if err := validateSchemaInner(propertyValue, sub, path+"."+name); err != nil {
					return err
				}
			}
		}
	}

	if required, ok := schema["required"]; ok {
		list, ok := required.([]any)
		if !ok {
			return fmt.Errorf("schema keyword 'required' must be an array")
		}
		if object, ok := value.(map[string]any); ok {
			for _, name := range list {
				nameStr, ok := name.(string)
				if !ok {
					return fmt.Errorf("schema 'required' entries must be strings")
				}
				if _, ok := object[nameStr]; !ok {
					return fmt.Errorf("missing required property '%s' at %s", nameStr, path)
				}
			}
		}
	}

	if items, ok := schema["items"]; ok {
		sub, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("schema keyword 'items' must be an object")
		}
		if array, ok := value.([]any); ok {
			for index, element := range array {
				if err := validateSchemaInner(element, sub, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// jsonTypeOf reports the JSON-schema type name of an encoding/json value.
// Integral float64s report "integer" (matching serde_json's Number split).
func jsonTypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if v == math.Trunc(v) && !math.IsInf(v, 0) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// typeMatches applies the one subtyping rule of the subset: "number"
// accepts integers.
func typeMatches(actual, expected string) bool {
	if expected == "number" && actual == "integer" {
		return true
	}
	return actual == expected
}

// schemaPromptSuffix renders the JSON-schema requirements appended to a
// schema-constrained agent's prompt (ody-rs append_schema_instruction).
func schemaPromptSuffix(schema map[string]any) string {
	var b strings.Builder
	b.WriteString("\n\nReply with JSON only (no prose, no markdown fences) matching this schema:\n")
	b.WriteString(prettyJSON(schema))
	return b.String()
}
