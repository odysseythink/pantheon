package core

import (
	"encoding/json"
	"testing"
)

type testOpts struct {
	Value string `json:"value"`
}

func (testOpts) ProviderName() string { return "test" }

func TestProviderOptions_GetSet(t *testing.T) {
	po := make(ProviderOptions)
	_, ok := po.Get("test")
	if ok {
		t.Error("expected missing key to return false")
	}

	po.Set("test", testOpts{Value: "hello"})
	v, ok := po.Get("test")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if to, ok := v.(testOpts); !ok || to.Value != "hello" {
		t.Errorf("unexpected value: %+v", v)
	}
}

func TestProviderOptions_MarshalJSON(t *testing.T) {
	po := make(ProviderOptions)
	po.Set("test", testOpts{Value: "hello"})
	data, err := json.Marshal(po)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d", len(m))
	}

	empty := make(ProviderOptions)
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("expected {}, got %s", string(data))
	}
}

func TestProviderOptions_UnmarshalJSON(t *testing.T) {
	po := make(ProviderOptions)
	err := po.UnmarshalJSON([]byte(`{"test": {"value": "hello"}}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// UnmarshalJSON is a no-op, so po should remain empty
	_, ok := po.Get("test")
	if ok {
		t.Error("expected UnmarshalJSON to be no-op")
	}
}
