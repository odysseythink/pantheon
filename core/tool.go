package core

import (
	"reflect"
	"strings"
)

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  *Schema
}

type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

type ToolChoiceMode string

const (
	ToolChoiceModeAuto     ToolChoiceMode = "auto"
	ToolChoiceModeRequired ToolChoiceMode = "required"
	ToolChoiceModeNone     ToolChoiceMode = "none"
)

type Schema struct {
	Type        string            `json:"type,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Items       *Schema           `json:"items,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
}

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
		}
		return s
	default:
		return &Schema{Type: "object"}
	}
}
