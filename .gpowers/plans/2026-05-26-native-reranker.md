# Native Reranker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `rerank.Provider` in `providers/native/` using cybertron's `BertForSequenceClassification` for local cross-encoder reranking, aligned with anything-llm's `NativeEmbeddingReranker`.

**Architecture:** Extend the existing `native` provider (which already implements `embed.Provider`) to also implement `rerank.Provider`. Add a `RerankModel` struct with `sync.Once` lazy-loading, manual `[CLS] query [SEP] doc [SEP]` token pair construction, direct `Model.Classify` inference (bypassing `TextClassification.Classify` to avoid Softmax), sigmoid scoring, and descending sort.

**Tech Stack:** Go 1.24, cybertron v0.2.1, spago v1.1.0 (existing dependencies — no new libraries).

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `providers/native/provider.go` | Modify | Add `rerank.Provider` interface implementation and `RerankModel` factory method |
| `providers/native/rerank.go` | Create | `RerankModel` struct, model loading, tokenization, truncation, inference, scoring, sorting |
| `providers/native/doc.go` | Modify | Update package documentation to mention reranker support |
| `providers/native/rerank_test.go` | Create | Unit tests for `RerankModel` (creation, empty inputs, model load errors) |

---

## Task 1: Modify `providers/native/provider.go`

**Files:**
- Modify: `providers/native/provider.go`

- [ ] **Step 1: Add `rerank` import and interface assertions**

Add `"github.com/odysseythink/pantheon/extensions/rerank"` to imports, and add `rerank.Provider` and `rerank.RerankModel` interface assertions to the `var` block.

```go
package native

import (
	"context"
	"errors"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/extensions/rerank" // 新增
)

var (
	_ core.Provider        = (*Provider)(nil)
	_ embed.Provider       = (*Provider)(nil)
	_ rerank.Provider      = (*Provider)(nil) // 新增
	_ embed.EmbeddingModel = (*EmbeddingModel)(nil)
	_ rerank.RerankModel   = (*RerankModel)(nil) // 新增
)
```

- [ ] **Step 2: Add `RerankModel` factory method**

Add the `RerankModel` method to `Provider`:

```go
// RerankModel creates a new native rerank model for the given model ID.
func (p *Provider) RerankModel(ctx context.Context, modelID string) (rerank.RerankModel, error) {
	return &RerankModel{
		provider: p,
		modelID:  modelID,
	}, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add providers/native/provider.go
git commit -m "feat(native): add rerank.Provider interface implementation"
```

---

## Task 2: Create `providers/native/rerank.go`

**Files:**
- Create: `providers/native/rerank.go`

- [ ] **Step 1: Create the file with imports and `RerankModel` struct**

```go
package native

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nlpodyssey/cybertron/pkg/models/bert"
	"github.com/nlpodyssey/cybertron/pkg/tasks"
	bert_textclassification "github.com/nlpodyssey/cybertron/pkg/tasks/textclassification/bert"
	"github.com/nlpodyssey/cybertron/pkg/tokenizers"
	"github.com/nlpodyssey/cybertron/pkg/tokenizers/wordpiecetokenizer"
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

// RerankModel implements rerank.RerankModel using a local BERT cross-encoder.
type RerankModel struct {
	provider    *Provider
	modelID     string
	doLowerCase bool

	once    sync.Once
	tc      *bert_textclassification.TextClassification
	loadErr error
}
```

- [ ] **Step 2: Add `loadModel` method**

```go
func (m *RerankModel) loadModel() error {
	m.once.Do(func() {
		modelDir := m.provider.modelDir
		modelName := m.modelID
		if modelName == "" {
			modelName = m.provider.modelName
		}

		conf := &tasks.Config{
			ModelsDir:           modelDir,
			ModelName:           modelName,
			DownloadPolicy:      tasks.DownloadNever,
			ConversionPolicy:    tasks.ConvertNever,
			ConversionPrecision: tasks.F32,
		}

		modelPath := conf.FullModelPath()

		// Read tokenizer config for doLowerCase (needed for tokenization consistency).
		tokenizerConfig, err := bert.ConfigFromFile[bert.TokenizerConfig](filepath.Join(modelPath, "tokenizer_config.json"))
		if err == nil {
			m.doLowerCase = tokenizerConfig.DoLowerCase
		}

		m.tc, m.loadErr = bert_textclassification.LoadTextClassification(modelPath)
	})

	if m.loadErr != nil {
		return fmt.Errorf("native rerank: failed to load model: %w", m.loadErr)
	}
	return nil
}
```

- [ ] **Step 3: Add `tokenizePair` and `truncate` methods**

```go
// tokenizePair builds a [CLS] query [SEP] doc [SEP] token sequence.
func (m *RerankModel) tokenizePair(query, doc string) []string {
	if m.doLowerCase {
		query = strings.ToLower(query)
		doc = strings.ToLower(doc)
	}

	queryTokens := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(query))
	docTokens := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(doc))

	tokens := make([]string, 0, 2+len(queryTokens)+len(docTokens))
	tokens = append(tokens, wordpiecetokenizer.DefaultClassToken)
	tokens = append(tokens, queryTokens...)
	tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
	tokens = append(tokens, docTokens...)
	tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
	return tokens
}

// truncate limits the token sequence to MaxPositionEmbeddings, preserving
// the query and truncating the document from the end if necessary.
func (m *RerankModel) truncate(tokens []string) []string {
	maxLen := m.tc.Model.Bert.Config.MaxPositionEmbeddings
	if len(tokens) <= maxLen {
		return tokens
	}

	sep := wordpiecetokenizer.DefaultSequenceSeparator

	// Find the first [SEP] to determine query length.
	firstSepIdx := -1
	for i, t := range tokens {
		if t == sep {
			firstSepIdx = i
			break
		}
	}
	if firstSepIdx == -1 {
		// No [SEP] found — truncate from the end as a fallback.
		return tokens[:maxLen]
	}

	queryLen := firstSepIdx + 1 // includes [CLS]...query...[SEP]
	if queryLen >= maxLen {
		// Query itself is too long; truncate query and keep closing [SEP].
		return append(tokens[:maxLen-1], sep)
	}

	// Truncate document from the end, keeping the final [SEP].
	docMaxLen := maxLen - queryLen - 1
	end := queryLen + docMaxLen
	if end >= len(tokens) {
		end = len(tokens) - 1
	}
	result := make([]string, 0, maxLen)
	result = append(result, tokens[:end]...)
	result = append(result, sep)
	return result
}
```

- [ ] **Step 4: Add `sigmoid` helper and `Rerank` method**

```go
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// Rerank scores and reorders documents by relevance to the query.
func (m *RerankModel) Rerank(ctx context.Context, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("native rerank: query is required")
	}
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("native rerank: documents cannot be empty")
	}

	if err := m.loadModel(); err != nil {
		return nil, err
	}

	results := make([]rerank.RerankResult, 0, len(req.Documents))
	for i, doc := range req.Documents {
		tokens := m.tokenizePair(req.Query, doc)
		tokens = m.truncate(tokens)

		logitTensor := m.tc.Model.Classify(tokens)
		logit := mat.Data[float64](logitTensor)[0]
		score := sigmoid(logit)

		result := rerank.RerankResult{
			Index:          i,
			RelevanceScore: float32(score),
		}
		if req.ReturnDocuments {
			result.Document = doc
		}
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].RelevanceScore > results[j].RelevanceScore
	})

	if req.TopN > 0 && req.TopN < len(results) {
		results = results[:req.TopN]
	}

	return &rerank.RerankResponse{
		Results: results,
		Usage:   core.Usage{},
	}, nil
}
```

注意：`mat` 包需要导入：`"github.com/nlpodyssey/spago/mat"`

- [ ] **Step 5: Commit**

```bash
git add providers/native/rerank.go
git commit -m "feat(native): implement RerankModel with cross-encoder inference"
```

---

## Task 3: Modify `providers/native/doc.go`

**Files:**
- Modify: `providers/native/doc.go`

- [ ] **Step 1: Update package documentation**

```go
// Package native provides a local embedding and reranker provider using the
// Cybertron library (github.com/nlpodyssey/cybertron), which runs BERT-based
// models in pure Go without CGO.
//
// Embedding models:
//   - sentence-transformers/all-MiniLM-L6-v2
//   - sentence-transformers/LaBSE
//   - Xenova/all-MiniLM-L6-v2
//   - nomic-ai/nomic-embed-text-v1
//   - intfloat/multilingual-e5-small
//
// Reranker models:
//   - cross-encoder/ms-marco-MiniLM-L-6-v2
//
// Models must be downloaded and converted to the Cybertron format beforehand.
// Use the cybertron CLI or the huggingface-go tools to prepare models.
package native
```

- [ ] **Step 2: Commit**

```bash
git add providers/native/doc.go
git commit -m "docs(native): update package doc to mention reranker support"
```

---

## Task 4: Create `providers/native/rerank_test.go`

**Files:**
- Create: `providers/native/rerank_test.go`

- [ ] **Step 1: Write all tests**

```go
package native

import (
	"context"
	"os"
	"testing"

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

func TestRerankModel_Rerank_ReturnDocuments(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")

	// Empty query fails before model loading, so we can't test ReturnDocuments
	// without a real model. This test verifies the error path behaves correctly.
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "",
		Documents:       []string{"doc1"},
		ReturnDocuments: true,
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

// TestRerankModel_Rerank_Integration runs an end-to-end rerank test against a
// real converted model. Skipped unless NATIVE_RERANK_MODEL_DIR is set.
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

	// Paris should be the top result.
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
```

- [ ] **Step 2: Commit**

```bash
git add providers/native/rerank_test.go
git commit -m "test(native): add RerankModel unit and integration tests"
```

---

## Task 5: Verify Build and Tests

**Files:**
- All of the above

- [ ] **Step 1: Build**

```bash
go build ./...
```

Expected: `go build ./...` passes with no errors.

- [ ] **Step 2: Run unit tests**

```bash
go test ./providers/native/ -v -run 'TestProvider_RerankModel|TestRerankModel_Rerank_EmptyQuery|TestRerankModel_Rerank_EmptyDocuments|TestRerankModel_Rerank_ModelNotFound|TestRerankModel_Rerank_ReturnDocuments'
```

Expected: All tests pass. Integration test is skipped.

- [ ] **Step 3: Run full native package tests**

```bash
go test ./providers/native/ -v
```

Expected: All tests pass. `TestEmbeddingModel_*` tests from existing `provider_test.go` continue to pass.

- [ ] **Step 4: Run vet**

```bash
go vet ./providers/native/
```

Expected: No issues.

- [ ] **Step 5: Final commit (if any fixes needed)**

```bash
git add -A
git commit -m "fix(native): address review feedback" # only if fixes needed
```

---

## Self-Review Checklist

**1. Spec coverage:**
- ✅ `rerank.Provider` implementation in `providers/native/` — Task 1
- ✅ `RerankModel` struct with `sync.Once` lazy-loading — Task 2 Step 1-2
- ✅ `[CLS] query [SEP] doc [SEP]` token pair construction — Task 2 Step 3
- ✅ Truncation based on `MaxPositionEmbeddings` — Task 2 Step 3
- ✅ Direct `Model.Classify` + sigmoid (bypassing Softmax) — Task 2 Step 4
- ✅ Descending sort + TopN slicing — Task 2 Step 4
- ✅ Empty query/documents validation — Task 4
- ✅ Model load error handling — Task 4
- ✅ Integration test (environment-variable controlled) — Task 4
- ✅ Package doc update — Task 3

**2. Placeholder scan:** No TBD/TODO/"implement later"/"add appropriate error handling" found.

**3. Type consistency:** All type names and method signatures match between tasks. `RerankModel` is consistently referenced. `bert_textclassification` import alias is used consistently.
