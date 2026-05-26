package embed_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

func newEmbedClient(t *testing.T) *openaicompat.Client {
	t.Helper()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
	client := openaicompat.NewClient("http://192.168.11.150:8989", apiKey)
	client.HTTPClient.Timeout = 60 * time.Second
	return client
}

func TestIntegration_Embed_Single(t *testing.T) {
	client := newEmbedClient(t)
	resp, err := client.CreateEmbeddings(context.Background(), "Qwen/Qwen3-Embedding-0.6B", []string{"hello world"})
	if err != nil {
		t.Fatalf("CreateEmbeddings failed: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) == 0 {
		t.Fatal("embedding vector is empty")
	}
	t.Logf("embedding dim: %d", len(resp.Embeddings[0]))
}

func TestIntegration_Embed_Batch(t *testing.T) {
	client := newEmbedClient(t)
	texts := []string{"hello world", "good morning", "nice to meet you"}
	resp, err := client.CreateEmbeddings(context.Background(), "Qwen/Qwen3-Embedding-0.6B", texts)
	if err != nil {
		t.Fatalf("CreateEmbeddings failed: %v", err)
	}
	if len(resp.Embeddings) != len(texts) {
		t.Fatalf("expected %d embeddings, got %d", len(texts), len(resp.Embeddings))
	}
	for i, emb := range resp.Embeddings {
		if len(emb) == 0 {
			t.Fatalf("embedding[%d] is empty", i)
		}
	}
	dim := len(resp.Embeddings[0])
	for i, emb := range resp.Embeddings {
		if len(emb) != dim {
			t.Fatalf("embedding[%d] dim %d != expected %d", i, len(emb), dim)
		}
	}
	t.Logf("batch embeddings: count=%d, dim=%d", len(resp.Embeddings), dim)
}

func TestIntegration_Embed_CosineSimilarity(t *testing.T) {
	client := newEmbedClient(t)
	texts := []string{"cat", "dog", "car"}
	resp, err := client.CreateEmbeddings(context.Background(), "Qwen/Qwen3-Embedding-0.6B", texts)
	if err != nil {
		t.Fatalf("CreateEmbeddings failed: %v", err)
	}
	if len(resp.Embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(resp.Embeddings))
	}

	// cat and dog should be more similar than cat and car
	simCatDog := embed.Cosine(resp.Embeddings[0], resp.Embeddings[1])
	simCatCar := embed.Cosine(resp.Embeddings[0], resp.Embeddings[2])
	t.Logf("cosine(cat,dog)=%.4f, cosine(cat,car)=%.4f", simCatDog, simCatCar)

	if simCatDog <= simCatCar {
		t.Fatalf("expected cat-dog similarity (%.4f) > cat-car similarity (%.4f)", simCatDog, simCatCar)
	}
}
