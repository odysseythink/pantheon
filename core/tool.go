package core

import (
	"encoding/json"
	"reflect"
	"strings"
)

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  *Schema
}

// ToolChoice controls whether and how the model may invoke tools.
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// ToolChoiceMode selects the tool invocation policy.
type ToolChoiceMode string

const (
	// ToolChoiceModeAuto lets the model decide whether to call a tool.
	ToolChoiceModeAuto ToolChoiceMode = "auto"
	// ToolChoiceModeRequired forces the model to call at least one tool.
	ToolChoiceModeRequired ToolChoiceMode = "required"
	// ToolChoiceModeNone prevents the model from calling any tools.
	ToolChoiceModeNone ToolChoiceMode = "none"
)

// Schema describes the shape of a JSON value.
type Schema struct {
	Type        string            `json:"type,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Items       *Schema           `json:"items,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
}

// GenerateSchema builds a JSON Schema from a Go reflect.Type.
func GenerateSchema(t reflect.Type) *Schema {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return generateSchemaFromType(t)
}

func generateSchemaFromType(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: generateSchemaFromType(t.Elem())}
	case reflect.Struct:
		s := &Schema{Type: "object", Properties: make(map[string]*Schema)}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("json"); tag != "" {
				parts := strings.Split(tag, ",")
				if parts[0] == "-" {
					continue
				}
				name = parts[0]
			}
			s.Properties[name] = generateSchemaFromType(field.Type)
			s.Required = append(s.Required, name)
		}
		return s
	default:
		return &Schema{Type: "object"}
	}
}

// SchemaFromJSON parses a JSON Schema from raw JSON bytes.
func SchemaFromJSON(data []byte) (*Schema, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var s Schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// MustSchemaFromJSON parses a JSON Schema from raw JSON bytes and panics on error.
func MustSchemaFromJSON(data []byte) *Schema {
	s, err := SchemaFromJSON(data)
	if err != nil {
		panic(err)
	}
	return s
}
