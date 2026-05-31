package compression

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestEngineRegistry_RegisterAndCreate(t *testing.T) {
	r := NewEngineRegistry()
	r.Register("test", func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error) {
		return NewDefaultCompressor(cfg, aux), nil
	})

	eng, err := r.Create("test", DefaultCompressionConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if eng.Name() != "default" {
		t.Fatalf("expected name 'default', got %q", eng.Name())
	}
}

func TestEngineRegistry_Create_NotFound(t *testing.T) {
	r := NewEngineRegistry()
	_, err := r.Create("missing", DefaultCompressionConfig(), nil)
	if err == nil {
		t.Fatal("expected error for missing engine")
	}
}

func TestEngineRegistry_DuplicatePanics(t *testing.T) {
	r := NewEngineRegistry()
	r.Register("dup", func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error) {
		return nil, nil
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register("dup", func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error) {
		return nil, nil
	})
}

func TestDefaultRegistry_HasDefault(t *testing.T) {
	eng, err := DefaultRegistry.Create("default", DefaultCompressionConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
}
