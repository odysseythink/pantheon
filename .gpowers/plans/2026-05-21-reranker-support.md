# Reranker Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add reranker model support to Pantheon via a new `extensions/rerank` package, multi-format HTTP adapter in `providers/openaicompat`, and `providers/openai` implementation.

**Architecture:** Replicate the `extensions/embed` design pattern: interface extension (`rerank.Provider` embedding `core.Provider`), HTTP client methods with format-switching (`RerankFormat` enum), and concrete provider implementation. Supports OpenAI-compatible, Cohere v2, and Jina APIs.

**Tech Stack:** Go 1.24, existing `core.HttpClientCall`, `httptest` for unit tests.

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `extensions/rerank/doc.go` | Create | Package documentation |
| `extensions/rerank/provider.go` | Create | `Provider`, `RerankModel`, `RerankRequest`, `RerankResponse`, `RerankResult` interfaces and types |
| `extensions/rerank/provider_test.go` | Create | Mock provider/model tests |
| `providers/openaicompat/client.go` | Modify | Add `RerankPath` and `RerankFormat` fields |
| `providers/openaicompat/rerank.go` | Create | `CreateRerank`, format resolution, OpenAI-compatible/Cohere v2/Jina adapters |
| `providers/openaicompat/rerank_test.go` | Create | Unit tests for all formats and validation |
| `providers/openai/provider.go` | Modify | Add `RerankModel` method to implement `rerank.Provider` |
| `providers/openai/model.go` | Modify | Add `RerankModel` struct and `Rerank` method |
| `providers/openai/model_test.go` | Modify | Add `TestRerank` delegation test |

---

## Task 1: extensions/rerank 接口与类型定义

**Files:**
- Create: `extensions/rerank/doc.go`
- Create: `extensions/rerank/provider.go`

- [ ] **Step 1: Create `extensions/rerank/doc.go`**

```go
// Package rerank provides a unified interface for reranker models
// that reorder documents by relevance to a query.
package rerank
```

- [ ] **Step 2: Create `extensions/rerank/provider.go`**

```go
package rerank

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Provider extends core.Provider with rerank capabilities.
type Provider interface {
	core.Provider
	RerankModel(ctx context.Context, modelID string) (RerankModel, error)
}

// RerankModel performs relevance-based reranking of documents against a query.
type RerankModel interface {
	Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error)
}

// RerankRequest holds parameters for a rerank operation.
// Fields align with Cohere Rerank v2 API for maximum compatibility.
type RerankRequest struct {
	Query           string
	Documents       []string
	TopN            int
	ReturnDocuments bool
	MaxChunksPerDoc int
	ProviderOptions core.ProviderOptions
}

// RerankResponse holds reranked results and token usage.
type RerankResponse struct {
	ID      string
	Results []RerankResult
	Usage   core.Usage
}

// RerankResult is a single reranked document with its relevance score.
type RerankResult struct {
	Index          int
	RelevanceScore float32
	Document       string
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./extensions/rerank`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add extensions/rerank/
git commit -m "feat(rerank): add reranker interface and types"
```

---

## Task 2: extensions/rerank mock 测试

**Files:**
- Create: `extensions/rerank/provider_test.go`

- [ ] **Step 1: Create `extensions/rerank/provider_test.go`**

```go
package rerank

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockRerankProvider struct{}

func (m *mockRerankProvider) Name() string { return "mock-rerank" }

func (m *mockRerankProvider) Models(ctx context.Context) ([]core.Model, error) {
	return nil, nil
}

func (m *mockRerankProvider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, nil
}

func (m *mockRerankProvider) RerankModel(ctx context.Context, modelID string) (RerankModel, error) {
	return &mockRerankModel{}, nil
}

type mockRerankModel struct{}

func (m *mockRerankModel) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	results := make([]RerankResult, len(req.Documents))
	for i := range req.Documents {
		results[i] = RerankResult{
			Index:          i,
			RelevanceScore: float32(len(req.Documents) - i),
			Document:       req.Documents[i],
		}
	}
	return &RerankResponse{
		ID:      "mock-id",
		Results: results,
		Usage:   core.Usage{PromptTokens: len(req.Documents) * 5, TotalTokens: len(req.Documents) * 5},
	}, nil
}

func TestRerank(t *testing.T) {
	p := &mockRerankProvider{}
	model, err := p.RerankModel(context.Background(), "mock-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := model.Rerank(context.Background(), &RerankRequest{
		Query:     "test query",
		Documents: []string{"doc a", "doc b", "doc c"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 0 {
		t.Errorf("result[0] index: got %d, want 0", resp.Results[0].Index)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("usage total: got %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestProviderImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*mockRerankProvider)(nil)
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./extensions/rerank -v`
Expected: PASS for `TestRerank` and `TestProviderImplementsCoreProvider`.

- [ ] **Step 3: Commit**

```bash
git add extensions/rerank/provider_test.go
git commit -m "test(rerank): add mock provider and model tests"
```

---

## Task 3: openaicompat 客户端扩展

**Files:**
- Modify: `providers/openaicompat/client.go`

- [ ] **Step 1: Modify `providers/openaicompat/client.go`**

Add `RerankFormat` type and constants before the `Client` struct, and add two fields to `Client`:

```go
// RerankFormat defines the API format for rerank requests.
type RerankFormat string

const (
	RerankFormatAuto             RerankFormat = "auto"
	RerankFormatOpenAICompatible RerankFormat = "openai"
	RerankFormatCohereV2         RerankFormat = "cohere"
	RerankFormatJina             RerankFormat = "jina"
)

// Client is a generic OpenAI-compatible HTTP client.
type Client struct {
	BaseURL            string
	APIKey             string
	HTTPClient         *http.Client
	Headers            map[string]string
	ChatCompletionPath string // default empty means "/v1/chat/completions"
	RerankPath         string // default empty means "/v1/rerank"
	RerankFormat       RerankFormat
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./providers/openaicompat`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/client.go
git commit -m "feat(openaicompat): add RerankPath and RerankFormat to Client"
```

---

## Task 4: openaicompat rerank OpenAI-compatible 格式

**Files:**
- Create: `providers/openaicompat/rerank.go`

- [ ] **Step 1: Create `providers/openaicompat/rerank.go` with OpenAI-compatible adapter**

```go
package openaicompat

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

// openaiRerankRequest is the request body for OpenAI-compatible rerank APIs.
type openaiRerankRequest struct {
	Model string   `json:"model"`
	Query string   `json:"query"`
	Docs  []string `json:"documents"`
	TopN  int      `json:"top_n,omitempty"`
}

// openaiRerankResponse is the response body for OpenAI-compatible rerank APIs.
type openaiRerankResponse struct {
	Model   string `json:"model"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document,omitempty"`
	} `json:"results"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func (c *Client) resolveRerankFormat() RerankFormat {
	if c.RerankFormat != "" && c.RerankFormat != RerankFormatAuto {
		return c.RerankFormat
	}
	if c.RerankPath == "/v2/rerank" {
		return RerankFormatCohereV2
	}
	return RerankFormatOpenAICompatible
}

// CreateRerank sends a rerank request to the API.
func (c *Client) CreateRerank(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("rerank: query is required")
	}
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("rerank: documents are required")
	}

	format := c.resolveRerankFormat()

	var resp *rerank.RerankResponse
	var err error

	switch format {
	case RerankFormatCohereV2:
		resp, err = c.createRerankCohere(ctx, model, req)
	case RerankFormatJina:
		resp, err = c.createRerankJina(ctx, model, req)
	default:
		resp, err = c.createRerankOpenAI(ctx, model, req)
	}

	if err != nil {
		return nil, fmt.Errorf("create rerank: %w", err)
	}
	return resp, nil
}

func (c *Client) createRerankOpenAI(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	body := openaiRerankRequest{
		Model: model,
		Query: req.Query,
		Docs:  req.Documents,
		TopN:  req.TopN,
	}

	path := c.RerankPath
	if path == "" {
		path = "/v1/rerank"
	}

	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}

	resp, err := core.HttpClientCall[openaiRerankResponse](
		ctx,
		"POST",
		c.BaseURL+path,
		nil,
		body,
		c.Headers,
	)
	if err != nil {
		return nil, err
	}

	results := make([]rerank.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		doc := ""
		if r.Document != nil {
			doc = r.Document.Text
		}
		results[i] = rerank.RerankResult{
			Index:          r.Index,
			RelevanceScore: float32(r.RelevanceScore),
			Document:       doc,
		}
	}

	return &rerank.RerankResponse{
		Results: results,
		Usage: core.Usage{
			PromptTokens: resp.Usage.PromptTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	}, nil
}

func (c *Client) createRerankJina(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	// Jina uses the same request/response format as OpenAI-compatible.
	return c.createRerankOpenAI(ctx, model, req)
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./providers/openaicompat`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/rerank.go
git commit -m "feat(openaicompat): add CreateRerank with OpenAI-compatible and Jina support"
```

---

## Task 5: openaicompat rerank Cohere v2 格式

**Files:**
- Modify: `providers/openaicompat/rerank.go`

- [ ] **Step 1: Append Cohere v2 structures and adapter to `providers/openaicompat/rerank.go`**

Add the following at the end of the file (after `createRerankJina`):

```go
// cohereRerankRequest is the request body for Cohere v2 rerank API.
type cohereRerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents,omitempty"`
	MaxChunksPerDoc int      `json:"max_chunks_per_doc,omitempty"`
}

// cohereRerankResponse is the response body for Cohere v2 rerank API.
type cohereRerankResponse struct {
	ID      string `json:"id"`
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       *struct {
			Text string `json:"text"`
		} `json:"document,omitempty"`
	} `json:"results"`
}

func (c *Client) createRerankCohere(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	body := cohereRerankRequest{
		Model:           model,
		Query:           req.Query,
		Documents:       req.Documents,
		TopN:            req.TopN,
		ReturnDocuments: req.ReturnDocuments,
		MaxChunksPerDoc: req.MaxChunksPerDoc,
	}

	path := c.RerankPath
	if path == "" {
		path = "/v2/rerank"
	}

	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}

	resp, err := core.HttpClientCall[cohereRerankResponse](
		ctx,
		"POST",
		c.BaseURL+path,
		nil,
		body,
		c.Headers,
	)
	if err != nil {
		return nil, err
	}

	results := make([]rerank.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		doc := ""
		if r.Document != nil {
			doc = r.Document.Text
		}
		results[i] = rerank.RerankResult{
			Index:          r.Index,
			RelevanceScore: float32(r.RelevanceScore),
			Document:       doc,
		}
	}

	return &rerank.RerankResponse{
		ID:      resp.ID,
		Results: results,
		Usage:   core.Usage{},
	}, nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `go build ./providers/openaicompat`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/rerank.go
git commit -m "feat(openaicompat): add Cohere v2 rerank adapter"
```

---

## Task 6: openaicompat rerank 单元测试

**Files:**
- Create: `providers/openaicompat/rerank_test.go`

- [ ] **Step 1: Create `providers/openaicompat/rerank_test.go`**

```go
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
```

- [ ] **Step 2: Run tests**

Run: `go test ./providers/openaicompat -run TestCreateRerank -v`
Expected: PASS for all four tests.

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/rerank_test.go
git commit -m "test(openaicompat): add rerank unit tests for OpenAI-compatible and Cohere v2"
```

---

## Task 7: openai provider 实现 rerank.Provider

**Files:**
- Modify: `providers/openai/provider.go`
- Modify: `providers/openai/model.go`

- [ ] **Step 1: Modify `providers/openai/provider.go`**

Add import for `rerank` package and `RerankModel` method:

```go
import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/extensions/rerank"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)
```

Append to the file (after `EmbeddingModel`):

```go
// RerankModel creates a new OpenAI rerank model for the given model ID.
func (p *Provider) RerankModel(ctx context.Context, modelID string) (rerank.RerankModel, error) {
	return &RerankModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 2: Modify `providers/openai/model.go`**

Add import for `rerank` package and append `RerankModel` struct:

```go
import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/extensions/rerank"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)
```

Append to the file (after `EmbeddingModel.Embed`):

```go
// RerankModel implements rerank.RerankModel for the OpenAI provider.
type RerankModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Rerank reorders documents by relevance to the query.
func (m *RerankModel) Rerank(ctx context.Context, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	return m.client.CreateRerank(ctx, m.model, req)
}
```

- [ ] **Step 3: Verify compilation**

Run: `go build ./providers/openai`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add providers/openai/provider.go providers/openai/model.go
git commit -m "feat(openai): implement rerank.Provider and RerankModel"
```

---

## Task 8: openai provider 测试

**Files:**
- Modify: `providers/openai/model_test.go`

- [ ] **Step 1: Append `TestRerank` to `providers/openai/model_test.go`**

Add import for `rerank` package:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/types"
)
```

Append at the end of the file:

```go
func TestRerank(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("unexpected path: %s", r.URL.Path)
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
				{Index: 0, RelevanceScore: 0.95, Document: &struct {
					Text string `json:"text"`
				}{Text: "doc a"}},
			},
			Usage: struct {
				PromptTokens int `json:"prompt_tokens"`
				TotalTokens  int `json:"total_tokens"`
			}{PromptTokens: 5, TotalTokens: 5},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	rp, ok := p.(rerank.Provider)
	if !ok {
		t.Fatal("expected provider to implement rerank.Provider")
	}
	model, _ := rp.RerankModel(context.Background(), "bge-reranker")
	resp, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "test",
		Documents:       []string{"doc a"},
		ReturnDocuments: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].Document != "doc a" {
		t.Errorf("unexpected document: %q", resp.Results[0].Document)
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./providers/openai -run TestRerank -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add providers/openai/model_test.go
git commit -m "test(openai): add RerankModel delegation test"
```

---

## Task 9: 最终验证

- [ ] **Step 1: Run all affected tests**

Run: `go test ./extensions/rerank/... ./providers/openaicompat/... ./providers/openai/...`
Expected: All PASS.

- [ ] **Step 2: Run go vet**

Run: `go vet ./extensions/rerank/... ./providers/openaicompat/... ./providers/openai/...`
Expected: No issues.

- [ ] **Step 3: Commit**

```bash
git commit --allow-empty -m "chore: complete reranker support implementation"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ `extensions/rerank/` 接口定义 → Task 1
- ✅ `extensions/rerank/` 测试 → Task 2
- ✅ `providers/openaicompat/client.go` 新增字段 → Task 3
- ✅ `providers/openaicompat/rerank.go` OpenAI-compatible 适配 → Task 4
- ✅ `providers/openaicompat/rerank.go` Cohere v2 适配 → Task 5
- ✅ `providers/openaicompat/rerank_test.go` 单元测试 → Task 6
- ✅ `providers/openai/` 实现 `rerank.Provider` → Task 7
- ✅ `providers/openai/` 测试 → Task 8
- ✅ 错误处理（空 query/documents）→ Task 4 Step 1 中已包含
- ✅ `RerankFormat` 枚举和 `auto` 启发式 → Task 3 + Task 4

**2. Placeholder scan:**
- ✅ 无 TBD、TODO、"implement later"、"fill in details"
- ✅ 无 "Add appropriate error handling" 等模糊描述
- ✅ 每个代码步骤都包含完整代码块
- ✅ 无 "Similar to Task N" 引用

**3. Type consistency:**
- ✅ `CreateRerank` 签名：`func (c *Client) CreateRerank(ctx context.Context, model string, req *rerank.RerankRequest) (*rerank.RerankResponse, error)` — 全计划一致
- ✅ `RerankModel.Rerank` 签名与接口定义一致
- ✅ `RerankResult` 字段名：`Index`, `RelevanceScore`, `Document` — 全计划一致
- ✅ `RerankFormat` 常量名：`RerankFormatAuto`, `RerankFormatOpenAICompatible`, `RerankFormatCohereV2`, `RerankFormatJina` — 全计划一致
