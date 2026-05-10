package core

import (
	"reflect"
	"testing"
)

type testPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type testPersonWithOptions struct {
	Name    string `json:"name,omitempty"`
	Age     int    `json:"age,omitempty"`
	Ignored string `json:"-"`
}

func TestGenerateSchema(t *testing.T) {
	schema := GenerateSchema(reflect.TypeOf(testPerson{}))
	if schema.Type != "object" {
		t.Errorf("type: got %q, want object", schema.Type)
	}
	if schema.Properties["name"].Type != "string" {
		t.Errorf("name type: got %q, want string", schema.Properties["name"].Type)
	}
	if schema.Properties["age"].Type != "integer" {
		t.Errorf("age type: got %q, want integer", schema.Properties["age"].Type)
	}
}

func TestGenerateSchemaJSONTagOptions(t *testing.T) {
	schema := GenerateSchema(reflect.TypeOf(testPersonWithOptions{}))
	if schema.Type != "object" {
		t.Errorf("type: got %q, want object", schema.Type)
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Errorf("missing property 'name'")
	}
	if _, ok := schema.Properties["name,omitempty"]; ok {
		t.Errorf("property key should not be 'name,omitempty'")
	}
	if _, ok := schema.Properties["-"]; ok {
		t.Errorf("property key should not be '-'")
	}
	if _, ok := schema.Properties["Ignored"]; ok {
		t.Errorf("ignored field should not appear in schema")
	}
}
