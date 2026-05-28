package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type testWeatherInput struct {
	Location string `json:"location" description:"City name"`
	Units    string `json:"units" enum:"celsius,fahrenheit"`
}

type testCalcInput struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

type testPersonResult struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestNewAgentTool_Schema(t *testing.T) {
	entry := NewAgentTool("weather", "Gets weather", func(ctx context.Context, input testWeatherInput) (any, error) {
		return "ok", nil
	})

	if entry.Name != "weather" {
		t.Errorf("name: got %q, want weather", entry.Name)
	}
	if entry.Description != "Gets weather" {
		t.Errorf("description: got %q, want Gets weather", entry.Description)
	}

	schema := entry.Schema.Parameters
	if schema.Type != "object" {
		t.Errorf("schema type: got %q, want object", schema.Type)
	}

	locProp, ok := schema.Properties["location"]
	if !ok {
		t.Fatal("missing 'location' property")
	}
	if locProp.Type != "string" {
		t.Errorf("location type: got %q, want string", locProp.Type)
	}
	if locProp.Description != "City name" {
		t.Errorf("location description: got %q, want City name", locProp.Description)
	}

	unitsProp, ok := schema.Properties["units"]
	if !ok {
		t.Fatal("missing 'units' property")
	}
	if len(unitsProp.Enum) != 2 {
		t.Fatalf("units enum: expected 2 values, got %v", unitsProp.Enum)
	}
	if unitsProp.Enum[0] != "celsius" || unitsProp.Enum[1] != "fahrenheit" {
		t.Errorf("units enum: got %v, want [celsius fahrenheit]", unitsProp.Enum)
	}

	if len(schema.Required) != 2 {
		t.Errorf("required: expected 2 fields, got %v", schema.Required)
	}
}

func TestNewAgentTool_Execute(t *testing.T) {
	entry := NewAgentTool("weather", "Gets weather", func(ctx context.Context, input testWeatherInput) (any, error) {
		temp := "22°C"
		if input.Units == "fahrenheit" {
			temp = "72°F"
		}
		return fmt.Sprintf("Weather in %s: %s", input.Location, temp), nil
	})

	args := json.RawMessage(`{"location":"San Francisco","units":"fahrenheit"}`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if result != "Weather in San Francisco: 72°F" {
		t.Errorf("result: got %q", result)
	}
}

func TestNewAgentTool_StructResult(t *testing.T) {
	entry := NewAgentTool("person", "Returns a person", func(ctx context.Context, input struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}) (any, error) {
		return testPersonResult{Name: input.Name, Age: input.Age}, nil
	})

	args := json.RawMessage(`{"name":"Alice","age":30}`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}

	var parsed testPersonResult
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result as JSON: %v", err)
	}
	if parsed.Name != "Alice" || parsed.Age != 30 {
		t.Errorf("parsed result: got %+v", parsed)
	}
}

func TestNewAgentTool_InvalidJSON(t *testing.T) {
	entry := NewAgentTool("weather", "Gets weather", func(ctx context.Context, input testWeatherInput) (any, error) {
		return "ok", nil
	})

	args := json.RawMessage(`{invalid json`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler should not return error, got: %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("result should be valid JSON: %v", err)
	}
	if payload["error"] == "" {
		t.Errorf("expected error payload, got %q", result)
	}
}

func TestNewAgentTool_FnError(t *testing.T) {
	entry := NewAgentTool("weather", "Gets weather", func(ctx context.Context, input testWeatherInput) (any, error) {
		return nil, errors.New("service unavailable")
	})

	args := json.RawMessage(`{"location":"NYC","units":"celsius"}`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("handler should not return error, got: %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("result should be valid JSON: %v", err)
	}
	if payload["error"] != "service unavailable" {
		t.Errorf("error payload: got %q, want service unavailable", payload["error"])
	}
}

func TestNewAgentTool_StringResult(t *testing.T) {
	entry := NewAgentTool("echo", "Echoes input", func(ctx context.Context, input struct {
		Msg string `json:"msg"`
	}) (any, error) {
		return input.Msg, nil
	})

	args := json.RawMessage(`{"msg":"hello"}`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	// tool.Result returns strings as-is
	if result != "hello" {
		t.Errorf("result: got %q, want hello", result)
	}
}

func TestNewParallelAgentTool(t *testing.T) {
	entry := NewParallelAgentTool("multiply", "Multiplies two numbers",
		func(ctx context.Context, input testCalcInput) (any, error) {
			return input.A * input.B, nil
		})

	if !entry.Parallel {
		t.Error("expected Parallel = true")
	}
	if entry.Name != "multiply" {
		t.Errorf("name: got %q, want multiply", entry.Name)
	}

	args := json.RawMessage(`{"a":3,"b":4}`)
	result, err := entry.Handler(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if result != "12" {
		t.Errorf("result: got %q, want 12", result)
	}
}

func TestNewAgentTool_Omitempty(t *testing.T) {
	type optionalInput struct {
		Required string `json:"required"`
		Optional string `json:"optional,omitempty"`
	}

	entry := NewAgentTool("test", "Test", func(ctx context.Context, input optionalInput) (any, error) {
		return "ok", nil
	})

	schema := entry.Schema.Parameters
	if len(schema.Required) != 1 || schema.Required[0] != "required" {
		t.Errorf("required: got %v, want [required]", schema.Required)
	}
}

func TestNewAgentTool_RegistryIntegration(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewAgentTool("add", "Adds two numbers",
		func(ctx context.Context, input testCalcInput) (any, error) {
			return input.A + input.B, nil
		}))

	args := json.RawMessage(`{"a":1.5,"b":2.5}`)
	result, err := registry.Dispatch(context.Background(), "add", args)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}
	if result != "4" {
		t.Errorf("result: got %q, want 4", result)
	}
}
