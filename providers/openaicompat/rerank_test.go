package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/extensions/rerank"
)

func TestCreateRerank_OpenAICompatible(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", auth)
		}

		var req openaiRerankRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "bge-reranker" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		if req.Query != "test query" {
			t.Errorf("unexpected query: %s", req.Query)
		}
		if len(req.Docs) != 2 {
			t.Errorf("unexpected documents count: %d", len(req.Docs))
		}

		resp := openaiRerankResponse{
			Model: "bge-reranker",
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
				Document       *struct {
					Text string `json:"text"`
				} `json:"document,omitempty"`
			}{
				{Index: 1, RelevanceScore: 0.95, Document: &struct {
					Text string `json:"text"`
				}{Text: "doc b"}},
				{Index: 0, RelevanceScore: 0.85, Document: &struct {
					Text string `json:"text"`
				}{Text: "doc a"}},
			},
			Usage: struct {
				PromptTokens int `json:"prompt_tokens"`
				TotalTokens  int `json:"total_tokens"`
			}{PromptTokens: 10, TotalTokens: 10},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	resp, err := client.CreateRerank(context.Background(), "bge-reranker", &rerank.RerankRequest{
		Query:           "test query",
		Documents:       []string{"doc a", "doc b"},
		TopN:            2,
		ReturnDocuments: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 1 {
		t.Errorf("result[0] index: got %d, want 1", resp.Results[0].Index)
	}
	if resp.Results[0].RelevanceScore != 0.95 {
		t.Errorf("result[0] score: got %f, want 0.95", resp.Results[0].RelevanceScore)
	}
	if resp.Results[0].Document != "doc b" {
		t.Errorf("result[0] document: got %q, want doc b", resp.Results[0].Document)
	}
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("usage total: got %d, want 10", resp.Usage.TotalTokens)
	}
}

func TestCreateRerank_CohereV2(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/rerank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var req cohereRerankRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "rerank-english-v3.0" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		if !req.ReturnDocuments {
			t.Error("expected ReturnDocuments=true")
		}
		if req.MaxChunksPerDoc != 5 {
			t.Errorf("unexpected max_chunks_per_doc: %d", req.MaxChunksPerDoc)
		}

		resp := cohereRerankResponse{
			ID: "cohere-id-123",
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
				Document       *struct {
					Text string `json:"text"`
				} `json:"document,omitempty"`
			}{
				{Index: 0, RelevanceScore: 0.99, Document: &struct {
					Text string `json:"text"`
				}{Text: "doc a"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	client.RerankPath = "/v2/rerank"
	client.RerankFormat = RerankFormatCohereV2

	resp, err := client.CreateRerank(context.Background(), "rerank-english-v3.0", &rerank.RerankRequest{
		Query:           "test query",
		Documents:       []string{"doc a"},
		ReturnDocuments: true,
		MaxChunksPerDoc: 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "cohere-id-123" {
		t.Errorf("id: got %q, want cohere-id-123", resp.ID)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("cohere usage should be zero, got %+v", resp.Usage)
	}
}

func TestCreateRerank_EmptyQuery(t *testing.T) {
	client := NewClient("http://localhost", "key")
	_, err := client.CreateRerank(context.Background(), "model", &rerank.RerankRequest{
		Query:     "",
		Documents: []string{"doc"},
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestCreateRerank_EmptyDocuments(t *testing.T) {
	client := NewClient("http://localhost", "key")
	_, err := client.CreateRerank(context.Background(), "model", &rerank.RerankRequest{
		Query:     "query",
		Documents: []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty documents")
	}
}
