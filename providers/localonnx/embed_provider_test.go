package localonnx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/extensions/embed"
)

// The adapter must satisfy pantheon's standard embed.Provider contract so
// any pantheon consumer can use on-device embeddings.
func TestProviderContract(t *testing.T) {
	var _ embed.Provider = (*Provider)(nil)

	p := &Provider{Root: t.TempDir()}
	if p.Name() != "localonnx" {
		t.Fatalf("unexpected name: %s", p.Name())
	}
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != len(Catalog()) {
		t.Fatalf("Models returned %d entries, want %d", len(models), len(Catalog()))
	}
	if models[0].ID != PresetModelIDs[0] {
		t.Fatalf("first model = %q, want %q", models[0].ID, PresetModelIDs[0])
	}
	if models[0].ContextWindow <= 0 {
		t.Fatalf("ContextWindow not mapped: %+v", models[0])
	}
}

// EmbeddingModel rejects unknown ids and never loads weights eagerly.
func TestProviderEmbeddingModelValidation(t *testing.T) {
	p := &Provider{Root: t.TempDir()}
	if _, err := p.EmbeddingModel(context.Background(), "no/such-model"); err == nil {
		t.Fatal("expected error for unknown model")
	}
	m, err := p.EmbeddingModel(context.Background(), PresetModelIDs[0])
	if err != nil {
		t.Fatalf("EmbeddingModel: %v", err)
	}
	if m == nil {
		t.Fatal("EmbeddingModel returned nil model")
	}
}

// Embed on an empty root must fail fast (runtime library missing), not hang
// or silently download anything.
func TestProviderEmbedMissingRuntime(t *testing.T) {
	p := &Provider{Root: t.TempDir()}
	m, err := p.EmbeddingModel(context.Background(), PresetModelIDs[0])
	if err != nil {
		t.Fatalf("EmbeddingModel: %v", err)
	}
	start := time.Now()
	_, err = m.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error when the runtime library is missing")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Embed took too long (%s); must fail fast", time.Since(start))
	}
}

// localonnx is embedding-only: LanguageModel must return a clear error.
func TestProviderLanguageModelUnsupported(t *testing.T) {
	p := &Provider{Root: t.TempDir()}
	_, err := p.LanguageModel(context.Background(), PresetModelIDs[0])
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "embedding-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}
