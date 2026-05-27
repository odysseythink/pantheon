package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestAddAdditionalPropertiesFalse(t *testing.T) {
	schema := &core.Schema{
		Type: "object",
		Properties: map[string]*core.Schema{
			"name": {Type: "string"},
			"address": {
				Type: "object",
				Properties: map[string]*core.Schema{
					"city": {Type: "string"},
				},
			},
			"tags": {
				Type: "array",
				Items: &core.Schema{
					Type: "object",
					Properties: map[string]*core.Schema{
						"key": {Type: "string"},
					},
				},
			},
		},
	}

	addAdditionalPropertiesFalse(schema)

	if schema.AdditionalProperties != false {
		t.Errorf("expected root schema additionalProperties=false, got %v", schema.AdditionalProperties)
	}
	if schema.Properties["address"].AdditionalProperties != false {
		t.Errorf("expected nested object additionalProperties=false, got %v", schema.Properties["address"].AdditionalProperties)
	}
	if schema.Properties["tags"].Items.AdditionalProperties != false {
		t.Errorf("expected items object additionalProperties=false, got %v", schema.Properties["tags"].Items.AdditionalProperties)
	}
}

func TestAddAdditionalPropertiesFalse_PreservesExisting(t *testing.T) {
	schema := &core.Schema{
		Type:                 "object",
		AdditionalProperties: true,
	}

	addAdditionalPropertiesFalse(schema)

	if schema.AdditionalProperties != true {
		t.Errorf("expected existing additionalProperties to be preserved, got %v", schema.AdditionalProperties)
	}
}

func TestAddAdditionalPropertiesFalse_NonObjectUnchanged(t *testing.T) {
	schema := &core.Schema{
		Type: "string",
	}

	addAdditionalPropertiesFalse(schema)

	if schema.AdditionalProperties != nil {
		t.Errorf("expected non-object schema to remain unchanged, got %v", schema.AdditionalProperties)
	}
}

func TestToOpenAIResponseFormat_JSONSchema_InjectsAdditionalProperties(t *testing.T) {
	schema := &core.Schema{
		Type: "object",
		Properties: map[string]*core.Schema{
			"name": {Type: "string"},
		},
	}
	rf := &core.ResponseFormat{
		Type:       core.ResponseFormatTypeJSONSchema,
		JSONSchema: schema,
	}

	result := toOpenAIResponseFormat(rf)
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	jsonSchema, ok := m["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("expected json_schema map, got %T", m["json_schema"])
	}

	// The schema should have been modified in-place
	if schema.AdditionalProperties != false {
		t.Error("expected schema to have additionalProperties=false after toOpenAIResponseFormat")
	}

	// Verify it serializes correctly
	data, err := json.Marshal(jsonSchema["schema"])
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if raw["additionalProperties"] != false {
		t.Errorf("expected serialized schema to contain additionalProperties=false, got %v", raw["additionalProperties"])
	}
}
