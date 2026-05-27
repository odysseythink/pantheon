package core

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"
	"unicode"
)

// ToolCall represents a single tool invocation.
type ToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // JSON-encoded arguments
}

// ToolResponse is the result of executing a tool.
type ToolResponse struct {
	Content  string
	IsError  bool
	StopTurn bool
}

// ExecutableProviderTool pairs a provider-native tool definition with a
// client-side execution function. The provider serializes the tool in its
// native format, but the agent executes the Run function locally.
type ExecutableProviderTool struct {
	Definition any // provider-native representation (opaque)
	Run        func(ctx context.Context, call ToolCall) (ToolResponse, error)
}

// ProviderDefinedTool represents a tool that is defined and executed by the provider.
type ProviderDefinedTool struct {
	ID   string
	Name string
	Args map[string]any
}

// IsProviderDefinedTool unwraps a value into a ProviderDefinedTool pointer.
// It accepts both ProviderDefinedTool value and *ProviderDefinedTool.
func IsProviderDefinedTool(v any) (*ProviderDefinedTool, bool) {
	if v == nil {
		return nil, false
	}
	switch t := v.(type) {
	case ProviderDefinedTool:
		return &t, true
	case *ProviderDefinedTool:
		return t, true
	default:
		return nil, false
	}
}

// ToolDefinition describes a tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  *Schema
	// ProviderTool, if non-nil, indicates a provider-native tool that is
	// executed server-side by the provider. The value is opaque to core
	// and is serialized directly in the provider's native wire format.
	ProviderTool any
	// ExecutableTool, if non-nil, indicates a provider-native tool that
	// uses the provider's wire format but is executed locally by the agent.
	ExecutableTool *ExecutableProviderTool
	// Parallel indicates whether this tool can be executed concurrently
	// with other parallel tools within the same step.
	Parallel bool
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
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Format               string             `json:"format,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
}

// GenerateSchema builds a JSON Schema from a Go reflect.Type.
//
// Supported mappings:
//   - string, int/uint/float, bool -> corresponding JSON Schema types
//   - []T, [N]T -> array with Items schema
//   - map[string]T -> object with additionalProperties
//   - struct -> object with Properties from exported fields
//   - time.Time -> string with format "date-time"
//
// Struct field tags:
//   - json:"name" -> use name as property key
//   - json:"name,omitempty" -> omit from Required
//   - json:"-" -> skip field
//   - description:"..." -> set Schema.Description
//   - enum:"a,b,c" -> set Schema.Enum
//
// Untagged fields use snake_case names.
func GenerateSchema(t reflect.Type) *Schema {
	if t == nil {
		return &Schema{Type: "object"}
	}
	return generateSchemaRecursive(t, make(map[reflect.Type]bool))
}

// GenerateSchemaFrom is a generic convenience wrapper for GenerateSchema.
func GenerateSchemaFrom[T any]() *Schema {
	var zero T
	return GenerateSchema(reflect.TypeOf(zero))
}

func generateSchemaRecursive(t reflect.Type, visited map[reflect.Type]bool) *Schema {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if visited[t] {
		return &Schema{Type: "object"}
	}

	// Handle time.Time specially
	if t == reflect.TypeOf(time.Time{}) {
		return &Schema{Type: "string", Format: "date-time"}
	}

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
		itemSchema := generateSchemaRecursive(t.Elem(), visited)
		return &Schema{Type: "array", Items: itemSchema}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			valueSchema := generateSchemaRecursive(t.Elem(), visited)
			return &Schema{
				Type:                 "object",
				AdditionalProperties: valueSchema,
			}
		}
		return &Schema{Type: "object"}
	case reflect.Struct:
		visited[t] = true
		defer delete(visited, t)

		schema := &Schema{
			Type:       "object",
			Properties: make(map[string]*Schema),
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}

			jsonTag := field.Tag.Get("json")
			if jsonTag == "-" {
				continue
			}

			fieldName := field.Name
			required := true

			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] != "" {
					fieldName = parts[0]
				}
				for _, part := range parts[1:] {
					if part == "omitempty" {
						required = false
						break
					}
				}
			} else {
				fieldName = toSnakeCase(fieldName)
			}

			fieldSchema := generateSchemaRecursive(field.Type, visited)

			if desc := field.Tag.Get("description"); desc != "" {
				fieldSchema.Description = desc
			}

			if enumTag := field.Tag.Get("enum"); enumTag != "" {
				enumValues := strings.Split(enumTag, ",")
				fieldSchema.Enum = make([]string, len(enumValues))
				for i, v := range enumValues {
					fieldSchema.Enum[i] = strings.TrimSpace(v)
				}
			}

			schema.Properties[fieldName] = fieldSchema
			if required {
				schema.Required = append(schema.Required, fieldName)
			}
		}
		return schema
	case reflect.Interface:
		return &Schema{Type: "object"}
	default:
		return &Schema{Type: "object"}
	}
}

func toSnakeCase(s string) string {
	var result []rune
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				result = append(result, '_')
			}
			result = append(result, unicode.ToLower(r))
		} else {
			result = append(result, r)
		}
	}
	return string(result)
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
