package native

import (
	"context"
	"os"
	"testing"

	"github.com/nlpodyssey/cybertron/pkg/tokenizers/wordpiecetokenizer"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

func TestProvider_RerankModel(t *testing.T) {
	p, err := New("/tmp/models", "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	model, err := prov.RerankModel(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected rerank model, got nil")
	}
}

func TestRerankModel_Rerank_EmptyQuery(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "",
		Documents: []string{"doc1", "doc2"},
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRerankModel_Rerank_EmptyDocuments(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "query",
		Documents: []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty documents")
	}
}

func TestRerankModel_Rerank_ModelNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	p, _ := New(tmpDir, "nonexistent-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "nonexistent-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "query",
		Documents: []string{"doc1"},
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRerankModel_Rerank_EmptyQuery_WithReturnDocuments(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "",
		Documents:       []string{"doc1"},
		ReturnDocuments: true,
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestBuildTokenPair(t *testing.T) {
	tests := []struct {
		name        string
		queryTokens []string
		docTokens   []string
		wantTokens  []string
		wantQueryLen int
	}{
		{
			name:         "simple pair",
			queryTokens:  []string{"hello", "world"},
			docTokens:    []string{"foo", "bar"},
			wantTokens:   []string{"[CLS]", "hello", "world", "[SEP]", "foo", "bar", "[SEP]"},
			wantQueryLen: 4,
		},
		{
			name:         "empty query",
			queryTokens:  []string{},
			docTokens:    []string{"foo"},
			wantTokens:   []string{"[CLS]", "[SEP]", "foo", "[SEP]"},
			wantQueryLen: 2,
		},
		{
			name:         "empty doc",
			queryTokens:  []string{"hello"},
			docTokens:    []string{},
			wantTokens:   []string{"[CLS]", "hello", "[SEP]", "[SEP]"},
			wantQueryLen: 3,
		},
		{
			name:         "literal SEP in query tokens",
			queryTokens:  []string{"a", "[SEP]", "b"},
			docTokens:    []string{"c"},
			wantTokens:   []string{"[CLS]", "a", "[SEP]", "b", "[SEP]", "c", "[SEP]"},
			wantQueryLen: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, queryLen := buildTokenPair(tt.queryTokens, tt.docTokens)
			if queryLen != tt.wantQueryLen {
				t.Errorf("queryLen = %d, want %d", queryLen, tt.wantQueryLen)
			}
			if len(tokens) != len(tt.wantTokens) {
				t.Fatalf("len(tokens) = %d, want %d; got %v", len(tokens), len(tt.wantTokens), tokens)
			}
			for i := range tokens {
				if tokens[i] != tt.wantTokens[i] {
					t.Errorf("tokens[%d] = %q, want %q", i, tokens[i], tt.wantTokens[i])
				}
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cls := wordpiecetokenizer.DefaultClassToken
	sep := wordpiecetokenizer.DefaultSequenceSeparator

	tests := []struct {
		name     string
		tokens   []string
		queryLen int
		maxLen   int
		want     []string
	}{
		{
			name:     "no truncation needed",
			tokens:   []string{cls, "a", sep, "b", sep},
			queryLen: 3,
			maxLen:   10,
			want:     []string{cls, "a", sep, "b", sep},
		},
		{
			name:     "truncate doc from end",
			tokens:   []string{cls, "a", "b", sep, "c", "d", "e", sep},
			queryLen: 4,
			maxLen:   6,
			want:     []string{cls, "a", "b", sep, "c", sep},
		},
		{
			name:     "exact maxLen",
			tokens:   []string{cls, "a", sep, "b", sep},
			queryLen: 3,
			maxLen:   5,
			want:     []string{cls, "a", sep, "b", sep},
		},
		{
			name:     "query too long",
			tokens:   []string{cls, "a", "b", "c", sep, "d", sep},
			queryLen: 5,
			maxLen:   4,
			want:     []string{cls, "a", "b", sep},
		},
		{
			name:     "zero doc space",
			tokens:   []string{cls, "a", "b", sep, "c", sep},
			queryLen: 4,
			maxLen:   5,
			want:     []string{cls, "a", "b", sep},
		},
		{
			name:     "no SEP found fallback",
			tokens:   []string{cls, "a", "b", "c"},
			queryLen: 4, // irrelevant since len <= maxLen in this case
			maxLen:   10,
			want:     []string{cls, "a", "b", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.tokens, tt.queryLen, tt.maxLen)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d; got %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSigmoid(t *testing.T) {
	// Sigmoid(0) = 0.5
	if got := sigmoid(0); got != 0.5 {
		t.Errorf("sigmoid(0) = %f, want 0.5", got)
	}
	// Large positive → 1
	if got := sigmoid(10); got < 0.9999 {
		t.Errorf("sigmoid(10) = %f, want ~1", got)
	}
	// Large negative → 0
	if got := sigmoid(-10); got > 0.0001 {
		t.Errorf("sigmoid(-10) = %f, want ~0", got)
	}
}

func TestRerankModel_Rerank_Integration(t *testing.T) {
	modelDir := os.Getenv("NATIVE_RERANK_MODEL_DIR")
	modelName := os.Getenv("NATIVE_RERANK_MODEL_NAME")
	if modelDir == "" || modelName == "" {
		t.Skip("set NATIVE_RERANK_MODEL_DIR and NATIVE_RERANK_MODEL_NAME to run integration test")
	}
	p, err := New(modelDir, modelName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	model, err := prov.RerankModel(context.Background(), modelName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "What is the capital of France?",
		Documents:       []string{"Paris is the capital of France.", "Berlin is the capital of Germany.", "Madrid is in Spain."},
		TopN:            2,
		ReturnDocuments: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 0 {
		t.Errorf("expected top result index 0 (Paris), got %d", resp.Results[0].Index)
	}
	if resp.Results[0].Document != "Paris is the capital of France." {
		t.Errorf("unexpected top document: %q", resp.Results[0].Document)
	}
	if resp.Results[0].RelevanceScore <= 0 || resp.Results[0].RelevanceScore > 1 {
		t.Errorf("expected score in (0,1], got %f", resp.Results[0].RelevanceScore)
	}
}
