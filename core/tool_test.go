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

func TestGenerateSchema_Pointer(t *testing.T) {
	typ := reflect.TypeOf(&testPerson{})
	schema := GenerateSchema(typ)
	if schema.Type != "object" {
		t.Errorf("expected object for pointer, got %q", schema.Type)
	}
}

func TestGenerateSchema_Float(t *testing.T) {
	type floats struct {
		F32 float32 `json:"f32"`
		F64 float64 `json:"f64"`
	}
	schema := GenerateSchema(reflect.TypeOf(floats{}))
	if schema.Properties["f32"].Type != "number" {
		t.Errorf("expected number for float32, got %q", schema.Properties["f32"].Type)
	}
	if schema.Properties["f64"].Type != "number" {
		t.Errorf("expected number for float64, got %q", schema.Properties["f64"].Type)
	}
}

func TestGenerateSchema_Bool(t *testing.T) {
	type booleans struct {
		Flag bool `json:"flag"`
	}
	schema := GenerateSchema(reflect.TypeOf(booleans{}))
	if schema.Properties["flag"].Type != "boolean" {
		t.Errorf("expected boolean, got %q", schema.Properties["flag"].Type)
	}
}

func TestGenerateSchema_Slice(t *testing.T) {
	type withSlice struct {
		Tags []string `json:"tags"`
	}
	schema := GenerateSchema(reflect.TypeOf(withSlice{}))
	if schema.Properties["tags"].Type != "array" {
		t.Errorf("expected array, got %q", schema.Properties["tags"].Type)
	}
	if schema.Properties["tags"].Items == nil || schema.Properties["tags"].Items.Type != "string" {
		t.Errorf("expected string items, got %+v", schema.Properties["tags"].Items)
	}
}

func TestGenerateSchema_Array(t *testing.T) {
	type withArray struct {
		Nums [3]int `json:"nums"`
	}
	schema := GenerateSchema(reflect.TypeOf(withArray{}))
	if schema.Properties["nums"].Type != "array" {
		t.Errorf("expected array, got %q", schema.Properties["nums"].Type)
	}
	if schema.Properties["nums"].Items == nil || schema.Properties["nums"].Items.Type != "integer" {
		t.Errorf("expected integer items, got %+v", schema.Properties["nums"].Items)
	}
}

func TestGenerateSchema_Default(t *testing.T) {
	type withMap struct {
		Data map[string]any `json:"data"`
	}
	schema := GenerateSchema(reflect.TypeOf(withMap{}))
	if schema.Properties["data"].Type != "object" {
		t.Errorf("expected object for map, got %q", schema.Properties["data"].Type)
	}
}

func TestGenerateSchema_NestedStruct(t *testing.T) {
	type Address struct {
		City string `json:"city"`
	}
	type Person struct {
		Name    string  `json:"name"`
		Address Address `json:"address"`
	}
	schema := GenerateSchema(reflect.TypeOf(Person{}))
	if schema.Properties["address"].Type != "object" {
		t.Errorf("expected object for nested struct, got %q", schema.Properties["address"].Type)
	}
	if schema.Properties["address"].Properties["city"].Type != "string" {
		t.Errorf("expected string for city, got %q", schema.Properties["address"].Properties["city"].Type)
	}
}

func TestGenerateSchema_PrivateField(t *testing.T) {
	type withPrivate struct {
		Name   string `json:"name"`
		secret string
	}
	schema := GenerateSchema(reflect.TypeOf(withPrivate{}))
	if _, ok := schema.Properties["secret"]; ok {
		t.Error("private field should not appear in schema")
	}
	if _, ok := schema.Properties["name"]; !ok {
		t.Error("public field should appear in schema")
	}
}
