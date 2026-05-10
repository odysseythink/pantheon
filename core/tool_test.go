package core

import (
	"reflect"
	"testing"
)

type testPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
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
