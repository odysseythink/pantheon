package openaicompat

import "github.com/odysseythink/pantheon/core"

// deepCopySchema returns a deep copy of the given schema.
func deepCopySchema(s *core.Schema) *core.Schema {
	if s == nil {
		return nil
	}
	cloned := &core.Schema{
		Type:                 s.Type,
		Description:          s.Description,
		AdditionalProperties: s.AdditionalProperties,
	}
	if s.Required != nil {
		cloned.Required = make([]string, len(s.Required))
		copy(cloned.Required, s.Required)
	}
	if s.Enum != nil {
		cloned.Enum = make([]string, len(s.Enum))
		copy(cloned.Enum, s.Enum)
	}
	if s.Properties != nil {
		cloned.Properties = make(map[string]*core.Schema, len(s.Properties))
		for k, v := range s.Properties {
			cloned.Properties[k] = deepCopySchema(v)
		}
	}
	cloned.Items = deepCopySchema(s.Items)
	return cloned
}

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
