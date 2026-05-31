package compression

import (
	"testing"
	"time"
)

func TestDefaultCompressor_ImplementsContextEngine(t *testing.T) {
	var _ ContextEngine = (*DefaultCompressor)(nil)
}

func TestDefaultCompressor_Name(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	if c.Name() != "default" {
		t.Fatalf("expected name 'default', got %q", c.Name())
	}
}

func TestDefaultCompressor_UpdateModel(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	if err := c.UpdateModel("gpt-4", 8192); err != nil {
		t.Fatal(err)
	}
	if c.contextLength != 8192 {
		t.Fatalf("expected contextLength 8192, got %d", c.contextLength)
	}
	if c.thresholdTokens != 4096 {
		t.Fatalf("expected thresholdTokens 4096, got %d", c.thresholdTokens)
	}
}

func TestDefaultCompressor_ShouldCompress(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), &mockModel{})
	c.UpdateModel("gpt-4", 8192)

	if c.ShouldCompress(4095) {
		t.Fatal("should not compress at 4095 tokens")
	}
	if !c.ShouldCompress(4097) {
		t.Fatal("should compress at 4097 tokens")
	}
}

func TestNewCompressor_BackwardCompat(t *testing.T) {
	c := NewCompressor(DefaultCompressionConfig(), &mockModel{})
	if c == nil {
		t.Fatal("NewCompressor returned nil")
	}
	var _ *DefaultCompressor = c
}

func TestCompressionConfig_WithDefaults(t *testing.T) {
	cfg := CompressionConfig{}.WithDefaults()
	if cfg.Threshold != 0.5 {
		t.Fatalf("expected Threshold 0.5, got %f", cfg.Threshold)
	}
	if cfg.ProtectLast != 20 {
		t.Fatalf("expected ProtectLast 20, got %d", cfg.ProtectLast)
	}
	if cfg.CooldownBase != 30*time.Second {
		t.Fatalf("expected CooldownBase 30s, got %v", cfg.CooldownBase)
	}
}

func TestDefaultCompressionConfig_Flags(t *testing.T) {
	cfg := DefaultCompressionConfig()
	if !cfg.Enabled {
		t.Fatal("expected Enabled true")
	}
	if !cfg.AntiThrashEnabled {
		t.Fatal("expected AntiThrashEnabled true")
	}
	if !cfg.CooldownEnabled {
		t.Fatal("expected CooldownEnabled true")
	}
	if !cfg.RedactionEnabled {
		t.Fatal("expected RedactionEnabled true")
	}
	if !cfg.ToolPruningEnabled {
		t.Fatal("expected ToolPruningEnabled true")
	}
	if !cfg.IterativeUpdateEnabled {
		t.Fatal("expected IterativeUpdateEnabled true")
	}
}
