package rerank_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/extensions/rerank"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

func newRerankClient(t *testing.T) *openaicompat.Client {
	t.Helper()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
	client := openaicompat.NewClient("http://192.168.11.150:8989", apiKey)
	client.HTTPClient.Timeout = 60 * time.Second
	return client
}

func TestIntegration_Rerank_Basic(t *testing.T) {
	client := newRerankClient(t)
	req := &rerank.RerankRequest{
		Query:     "hello",
		Documents: []string{"hello world", "goodbye world", "hello there"},
	}
	resp, err := client.CreateRerank(context.Background(), "Qwen/Qwen3-Reranker-0.6B", req)
	if err != nil {
		t.Fatalf("CreateRerank failed: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected rerank results, got none")
	}
	for i, r := range resp.Results {
		t.Logf("result[%d]: index=%d score=%.4f doc=%q", i, r.Index, r.RelevanceScore, r.Document)
	}
	// The most relevant should be one of the "hello" documents
	top := resp.Results[0]
	if top.Index != 0 && top.Index != 2 {
		t.Fatalf("expected top result to be index 0 or 2 (hello docs), got index %d", top.Index)
	}
	if top.RelevanceScore <= 0 {
		t.Fatalf("expected positive relevance score, got %.4f", top.RelevanceScore)
	}
}

func TestIntegration_Rerank_TopN(t *testing.T) {
	client := newRerankClient(t)
	req := &rerank.RerankRequest{
		Query:     "machine learning",
		Documents: []string{"deep learning", "cooking recipes", "neural networks", "gardening tips"},
		TopN:      2,
	}
	resp, err := client.CreateRerank(context.Background(), "Qwen/Qwen3-Reranker-0.6B", req)
	if err != nil {
		t.Fatalf("CreateRerank failed: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results with TopN=2, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		t.Logf("topN result[%d]: index=%d score=%.4f", i, r.Index, r.RelevanceScore)
	}
}

func TestIntegration_Rerank_EmptyQuery(t *testing.T) {
	client := newRerankClient(t)
	req := &rerank.RerankRequest{
		Query:     "",
		Documents: []string{"hello world"},
	}
	_, err := client.CreateRerank(context.Background(), "Qwen/Qwen3-Reranker-0.6B", req)
	if err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
	t.Logf("empty query error: %v", err)
}

func TestIntegration_Rerank_EmptyDocuments(t *testing.T) {
	client := newRerankClient(t)
	req := &rerank.RerankRequest{
		Query:     "hello",
		Documents: []string{},
	}
	_, err := client.CreateRerank(context.Background(), "Qwen/Qwen3-Reranker-0.6B", req)
	if err == nil {
		t.Fatal("expected error for empty documents, got nil")
	}
	t.Logf("empty documents error: %v", err)
}
