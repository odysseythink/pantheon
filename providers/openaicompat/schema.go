package openaicompat

import "github.com/odysseythink/pantheon/core"

// addAdditionalPropertiesFalse recursively adds "additionalProperties": false
// to all object schemas that don't already have it set. This is required by
// OpenAI's strict mode for structured outputs.
func addAdditionalPropertiesFalse(schema *core.Schema) {
	if schema == nil {
		return
	}
	if schema.Type == "object" && schema.AdditionalProperties == nil {
		schema.AdditionalProperties = false
	}
	for _, prop := range schema.Properties {
		addAdditionalPropertiesFalse(prop)
	}
	addAdditionalPropertiesFalse(schema.Items)
}
