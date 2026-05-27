package core

import (
	"reflect"
	"testing"
	"time"
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

type testPersonWithSnakeCase struct {
	UserName string
	Email    string
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

func TestGenerateSchema_SnakeCase(t *testing.T) {
	schema := GenerateSchema(reflect.TypeOf(testPersonWithSnakeCase{}))
	if _, ok := schema.Properties["user_name"]; !ok {
		t.Errorf("expected snake_case property 'user_name', got %+v", schema.Properties)
	}
	if _, ok := schema.Properties["email"]; !ok {
		t.Errorf("expected snake_case property 'email', got %+v", schema.Properties)
	}
}

func TestGenerateSchema_OmitRequired(t *testing.T) {
	schema := GenerateSchema(reflect.TypeOf(testPersonWithOptions{}))
	for _, req := range schema.Required {
		if req == "name" || req == "age" {
			t.Errorf("omitempty fields should not be required, got %q in required", req)
		}
	}
}

func TestGenerateSchema_DescriptionTag(t *testing.T) {
	type withDesc struct {
		Name string `json:"name" description:"The person's full name"`
	}
	schema := GenerateSchema(reflect.TypeOf(withDesc{}))
	if schema.Properties["name"].Description != "The person's full name" {
		t.Errorf("expected description, got %q", schema.Properties["name"].Description)
	}
}

func TestGenerateSchema_EnumTag(t *testing.T) {
	type withEnum struct {
		Color string `json:"color" enum:"red,green,blue"`
	}
	schema := GenerateSchema(reflect.TypeOf(withEnum{}))
	want := []string{"red", "green", "blue"}
	if len(schema.Properties["color"].Enum) != len(want) {
		t.Fatalf("expected enum %v, got %v", want, schema.Properties["color"].Enum)
	}
	for i, v := range want {
		if schema.Properties["color"].Enum[i] != v {
			t.Errorf("enum[%d]: got %q, want %q", i, schema.Properties["color"].Enum[i], v)
		}
	}
}

func TestGenerateSchema_Time(t *testing.T) {
	type withTime struct {
		CreatedAt time.Time `json:"created_at"`
	}
	schema := GenerateSchema(reflect.TypeOf(withTime{}))
	prop := schema.Properties["created_at"]
	if prop.Type != "string" || prop.Format != "date-time" {
		t.Errorf("expected string/date-time for time.Time, got %q/%q", prop.Type, prop.Format)
	}
}

func TestGenerateSchema_MapStringKey(t *testing.T) {
	type withMap struct {
		Data map[string]int `json:"data"`
	}
	schema := GenerateSchema(reflect.TypeOf(withMap{}))
	prop := schema.Properties["data"]
	if prop.Type != "object" {
		t.Errorf("expected object for map[string]int, got %q", prop.Type)
	}
	if prop.AdditionalProperties == nil {
		t.Error("expected AdditionalProperties for map[string]int")
	}
	addProp, ok := prop.AdditionalProperties.(*Schema)
	if !ok || addProp.Type != "integer" {
		t.Errorf("expected integer additionalProperties, got %+v", prop.AdditionalProperties)
	}
}

func TestGenerateSchema_CircularReference(t *testing.T) {
	type Node struct {
		Value int    `json:"value"`
		Next  *Node  `json:"next,omitempty"`
	}
	schema := GenerateSchema(reflect.TypeOf(Node{}))
	if schema.Properties["next"].Type != "object" {
		t.Errorf("expected object for circular ref, got %q", schema.Properties["next"].Type)
	}
}

func TestGenerateSchemaFrom(t *testing.T) {
	schema := GenerateSchemaFrom[testPerson]()
	if schema.Type != "object" {
		t.Errorf("expected object, got %q", schema.Type)
	}
	if schema.Properties["name"].Type != "string" {
		t.Errorf("expected string for name, got %q", schema.Properties["name"].Type)
	}
}

func TestGenerateSchema_Nil(t *testing.T) {
	schema := GenerateSchema(nil)
	if schema.Type != "object" {
		t.Errorf("expected object for nil type, got %q", schema.Type)
	}
}
