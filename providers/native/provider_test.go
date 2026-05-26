package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNew_MissingDir(t *testing.T) {
	_, err := New("", "test-model")
	if err == nil {
		t.Fatal("expected error for empty modelDir")
	}
}

func TestNew_MissingName(t *testing.T) {
	_, err := New("/tmp/models", "")
	if err == nil {
		t.Fatal("expected error for empty modelName")
	}
}

func TestNew(t *testing.T) {
	p, err := New("/tmp/models", "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider, got nil")
	}
}

func TestProvider_Models(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 5 {
		t.Fatalf("expected 5 models, got %d", len(models))
	}
	expectedIDs := []string{
		"sentence-transformers/all-MiniLM-L6-v2",
		"sentence-transformers/LaBSE",
		"Xenova/all-MiniLM-L6-v2",
		"nomic-ai/nomic-embed-text-v1",
		"intfloat/multilingual-e5-small",
	}
	for i, id := range expectedIDs {
		if models[i].ID != id {
			t.Fatalf("expected model ID %q at index %d, got %q", id, i, models[i].ID)
		}
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	_, err := p.LanguageModel(context.Background(), "any-model")
	if err == nil {
		t.Fatal("expected error for LanguageModel")
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed_ModelNotFound(t *testing.T) {
	// Use a temporary directory that does not contain a valid model.
	tmpDir := t.TempDir()
	p, _ := New(tmpDir, "nonexistent-model")
	prov := p.(*Provider)
	embedModel, _ := prov.EmbeddingModel(context.Background(), "nonexistent-model")
	_, err := embedModel.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error when model is not found")
	}
}

func TestEmbeddingModel_Embed_FallsBackToProviderModelName(t *testing.T) {
	tmpDir := t.TempDir()
	p, _ := New(tmpDir, "fallback-model")
	prov := p.(*Provider)
	// Pass empty modelID to trigger fallback to provider.modelName
	embedModel, _ := prov.EmbeddingModel(context.Background(), "")
	_, err := embedModel.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error when fallback model is not found")
	}
}

func TestEmbeddingModel_Embed_WithMockModel(t *testing.T) {
	// Create a minimal model directory structure that cybertron can attempt to load.
	// This test verifies the code path up to model loading without requiring a real model.
	tmpDir := t.TempDir()
	modelPath := filepath.Join(tmpDir, "mock-model")
	if err := os.MkdirAll(modelPath, 0755); err != nil {
		t.Fatalf("failed to create model dir: %v", err)
	}

	// cybertron loader expects vocab.txt and tokenizer_config.json and spago_model.bin
	// We intentionally leave them empty/missing to trigger a loading error.
	p, _ := New(tmpDir, "mock-model")
	prov := p.(*Provider)
	embedModel, _ := prov.EmbeddingModel(context.Background(), "mock-model")
	_, err := embedModel.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error when model files are missing/invalid")
	}
}
