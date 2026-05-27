package core

import (
	"context"
	"testing"
)

func TestStreamObjectResult_Object(t *testing.T) {
	stream := func(yield func(*ObjectStreamPart, error) bool) {
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: map[string]any{"name": "Alice", "age": 30.0}}, nil)
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: map[string]any{"name": "Alice", "age": 30.0}}, nil) // duplicate
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeFinish, FinishReason: "stop", Usage: &Usage{TotalTokens: 10}}, nil)
	}

	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	result := NewStreamObjectResult[Person](context.Background(), stream)
	obj, err := result.Object()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj.Object.Name != "Alice" {
		t.Errorf("name: got %q, want Alice", obj.Object.Name)
	}
	if obj.Object.Age != 30 {
		t.Errorf("age: got %d, want 30", obj.Object.Age)
	}
	if obj.FinishReason != "stop" {
		t.Errorf("finish reason: got %q, want stop", obj.FinishReason)
	}
	if obj.Usage.TotalTokens != 10 {
		t.Errorf("usage: got %d, want 10", obj.Usage.TotalTokens)
	}
}

func TestStreamObjectResult_PartialObjectStream(t *testing.T) {
	stream := func(yield func(*ObjectStreamPart, error) bool) {
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: map[string]any{"name": "A"}}, nil)
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: map[string]any{"name": "A", "age": 1.0}}, nil)
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: map[string]any{"name": "A", "age": 1.0}}, nil) // dup
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeFinish, FinishReason: "stop"}, nil)
	}

	type Person struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}

	result := NewStreamObjectResult[Person](context.Background(), stream)
	var objects []Person
	for p := range result.PartialObjectStream() {
		objects = append(objects, p)
	}

	if len(objects) != 2 {
		t.Fatalf("objects: got %d, want 2", len(objects))
	}
	if objects[0].Name != "A" || objects[0].Age != 0 {
		t.Errorf("first object: got %+v", objects[0])
	}
	if objects[1].Name != "A" || objects[1].Age != 1 {
		t.Errorf("second object: got %+v", objects[1])
	}
}

func TestStreamObjectResult_TextStream(t *testing.T) {
	stream := func(yield func(*ObjectStreamPart, error) bool) {
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeTextDelta, TextDelta: "Hello"}, nil)
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeTextDelta, TextDelta: " World"}, nil)
		yield(&ObjectStreamPart{Type: ObjectStreamPartTypeFinish, FinishReason: "stop"}, nil)
	}

	result := NewStreamObjectResult[map[string]any](context.Background(), stream)
	var text string
	for delta := range result.TextStream() {
		text += delta
	}
	if text != "Hello World" {
		t.Errorf("text: got %q, want 'Hello World'", text)
	}
}
