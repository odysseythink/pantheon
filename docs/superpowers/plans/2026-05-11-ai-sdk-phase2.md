# AI SDK Phase 2 — Extensions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build four composable extension packages (`errors/`, `retry/`, `fallback/`, `embed/`) that wrap `core.LanguageModel` without modifying core interfaces.

**Architecture:** All extensions are pure composition wrappers. `errors.Classify` provides unified error taxonomy used by both `retry` (decides whether to retry) and `fallback` (decides whether to switch candidate). `retry.Model` and `fallback.Model` both implement `core.LanguageModel`. `embed` defines a separate `EmbeddingModel` interface since embedding is a distinct capability from text generation.

**Tech Stack:** Go 1.23+, standard library (`context`, `time`, `math/rand/v2`, `errors`, `io`), existing `core/` and `providers/` packages.

---

## File Structure

```
ai/
├── extensions/
│   ├── errors/
│   │   ├── classifier.go      # Error taxonomy + Classify function
│   │   └── classifier_test.go # Classification tests
│   ├── retry/
│   │   ├── model.go           # Retry wrapper for LanguageModel
│   │   └── model_test.go      # Retry logic tests with mock model
│   ├── fallback/
│   │   ├── model.go           # Fallback wrapper with candidate list
│   │   └── model_test.go      # Fallback logic tests
│   └── embed/
│       ├── provider.go        # Provider + EmbeddingModel interfaces
│       └── provider_test.go   # Interface verification
```

---

## Task 1: Error Classifier

**Files:**
- Create: `extensions/errors/classifier.go`
- Create: `extensions/errors/classifier_test.go`

**Prerequisites:** `core/` package must be built (ProviderError with Status field).

- [ ] **Step 1: Write the failing test**

Create `extensions/errors/classifier_test.go`:

```go
package errors

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantKind   Kind
		wantRetry  bool
	}{
		{"rate limit 429", &core.ProviderError{Status: 429}, KindRateLimit, true},
		{"server error 500", &core.ProviderError{Status: 500}, KindServerError, true},
		{"server error 502", &core.ProviderError{Status: 502}, KindServerError, true},
		{"auth 401", &core.ProviderError{Status: 401}, KindAuth, false},
		{"auth 403", &core.ProviderError{Status: 403}, KindAuth, false},
		{"invalid request 400", &core.ProviderError{Status: 400}, KindInvalidRequest, false},
		{"context too long 413", &core.ProviderError{Status: 413}, KindContextTooLong, false},
		{"timeout 408", &core.ProviderError{Status: 408}, KindTimeout, true},
		{"context canceled", context.Canceled, KindTimeout, false},
		{"deadline exceeded", context.DeadlineExceeded, KindTimeout, false},
		{"unexpected EOF", io.ErrUnexpectedEOF, KindServerError, true},
		{"unknown error", errors.New("something else"), KindUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Classify(tt.err)
			if c.Kind != tt.wantKind {
				t.Errorf("kind: got %q, want %q", c.Kind, tt.wantKind)
			}
			if c.Retryable != tt.wantRetry {
				t.Errorf("retryable: got %v, want %v", c.Retryable, tt.wantRetry)
			}
		})
	}
}

func TestClassifyContextTooLongMessage(t *testing.T) {
	err := &core.ProviderError{Status: 400, Message: "context length exceeded"}
	c := Classify(err)
	if c.Kind != KindContextTooLong {
		t.Errorf("kind: got %q, want %q", c.Kind, KindContextTooLong)
	}
	if c.Retryable {
		t.Error("context too long should not be retryable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./extensions/errors -v
```

Expected: FAIL — `Kind`, `Classify`, `Classification` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `extensions/errors/classifier.go`:

```go
package errors

import (
	"context"
	"errors"
	"io"

	"github.com/odysseythink/pantheon/core"
)

type Kind string

const (
	KindRateLimit      Kind = "rate_limit"
	KindAuth           Kind = "auth"
	KindTimeout        Kind = "timeout"
	KindServerError    Kind = "server_error"
	KindContextTooLong Kind = "context_too_long"
	KindInvalidRequest Kind = "invalid_request"
	KindUnknown        Kind = "unknown"
)

type Classification struct {
	Kind      Kind
	Retryable bool
}

// Classify determines the error kind and whether a retry might succeed.
func Classify(err error) Classification {
	if err == nil {
		return Classification{Kind: KindUnknown, Retryable: false}
	}

	// Context cancellation is always non-retryable.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Classification{Kind: KindTimeout, Retryable: false}
	}

	// Unexpected EOF during streaming is retryable.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return Classification{Kind: KindServerError, Retryable: true}
	}

	// Provider errors are classified by HTTP status.
	var pe *core.ProviderError
	if errors.As(err, &pe) {
		return classifyProviderError(pe)
	}

	return Classification{Kind: KindUnknown, Retryable: false}
}

func classifyProviderError(pe *core.ProviderError) Classification {
	switch pe.Status {
	case 429:
		return Classification{Kind: KindRateLimit, Retryable: true}
	case 408:
		return Classification{Kind: KindTimeout, Retryable: true}
	case 500, 502, 503, 504:
		return Classification{Kind: KindServerError, Retryable: true}
	case 401, 403:
		return Classification{Kind: KindAuth, Retryable: false}
	case 413:
		return Classification{Kind: KindContextTooLong, Retryable: false}
	case 400:
		if pe.IsContextTooLong() {
			return Classification{Kind: KindContextTooLong, Retryable: false}
		}
		return Classification{Kind: KindInvalidRequest, Retryable: false}
	case 409:
		return Classification{Kind: KindServerError, Retryable: true}
	default:
		return Classification{Kind: KindUnknown, Retryable: false}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./extensions/errors -v
```

Expected: PASS for all sub-tests.

- [ ] **Step 5: Commit**

```bash
git add extensions/errors/
git commit -m "feat(extensions/errors): add error classifier"
```

---

## Task 2: Retry Extension

**Files:**
- Create: `extensions/retry/model.go`
- Create: `extensions/retry/model_test.go`

**Prerequisites:** Task 1 complete (`extensions/errors/`).

- [ ] **Step 1: Write the failing test**

Create `extensions/retry/model_test.go`:

```go
package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

type mockModel struct {
	calls     int
	failNextN int
	responses []*core.Response
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	if m.failNextN > 0 {
		m.failNextN--
		return nil, &core.ProviderError{Status: 500, Message: "server error"}
	}
	if len(m.responses) > 0 {
		r := m.responses[0]
		m.responses = m.responses[1:]
		return r, nil
	}
	return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "ok"}}}}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	m.calls++
	if m.failNextN > 0 {
		m.failNextN--
		return nil, &core.ProviderError{Status: 500, Message: "server error"}
	}
	return func(yield func(*core.StreamPart, error) bool) {
		yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: "ok"}, nil)
	}, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock-model" }

func TestGenerateRetriesOnFailure(t *testing.T) {
	inner := &mockModel{failNextN: 2}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		Multiplier: 2.0,
	}

	resp, err := m.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls != 3 {
		t.Errorf("calls: got %d, want 3", inner.calls)
	}
	if len(resp.Message.Content) != 1 {
		t.Errorf("expected 1 content part, got %d", len(resp.Message.Content))
	}
}

func TestGenerateExhaustsRetries(t *testing.T) {
	inner := &mockModel{failNextN: 5}
	m := &Model{
		Inner:      inner,
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		Multiplier: 2.0,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if inner.calls != 3 { // initial + 2 retries
		t.Errorf("calls: got %d, want 3", inner.calls)
	}
}

func TestGenerateNoRetryOnAuthError(t *testing.T) {
	inner := &mockModel{}
	inner.failNextN = 1 // will return 500, but let's make a custom mock for auth
	// Use a model that always returns auth error
	authModel := &mockModel{}
	m := &Model{
		Inner:      &alwaysAuth{authModel},
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if authModel.calls != 1 {
		t.Errorf("calls: got %d, want 1 (no retry on auth)", authModel.calls)
	}
}

type alwaysAuth struct {
	*mockModel
}

func (a *alwaysAuth) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	a.mockModel.calls++
	return nil, &core.ProviderError{Status: 401, Message: "unauthorized"}
}

func TestStreamRetriesOnInitFailure(t *testing.T) {
	inner := &mockModel{failNextN: 1}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	stream, err := m.Stream(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if inner.calls != 2 {
		t.Errorf("calls: got %d, want 2", inner.calls)
	}

	var got string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeTextDelta {
			got += part.TextDelta
		}
	}
	if got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./extensions/retry -v
```

Expected: FAIL — `Model`, `Inner`, `MaxRetries`, etc. undefined.

- [ ] **Step 3: Write implementation**

Create `extensions/retry/model.go`:

```go
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/odysseythink/pantheon/core"
	extErrors "github.com/odysseythink/pantheon/extensions/errors"
)

// Model wraps a core.LanguageModel with exponential backoff retry.
type Model struct {
	Inner      core.LanguageModel
	MaxRetries int
	BaseDelay  time.Duration
	Multiplier float64
}

func (m *Model) Provider() string { return m.Inner.Provider() }
func (m *Model) Model() string    { return m.Inner.Model() }

func (m *Model) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		resp, err := m.Inner.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		stream, err := m.Inner.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		resp, err := m.Inner.GenerateObject(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= m.MaxRetries; attempt++ {
		stream, err := m.Inner.StreamObject(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == m.MaxRetries {
			break
		}
		if !m.shouldRetry(err) {
			break
		}
		if err := m.sleep(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (m *Model) shouldRetry(err error) bool {
	c := extErrors.Classify(err)
	return c.Retryable
}

func (m *Model) sleep(ctx context.Context, attempt int) error {
	base := m.BaseDelay
	if base <= 0 {
		base = 1 * time.Second
	}
	mult := m.Multiplier
	if mult <= 1 {
		mult = 2.0
	}

	delay := base
	for i := 0; i < attempt; i++ {
		delay = time.Duration(float64(delay) * mult)
	}
	// Add jitter: ±25%
	jitter := time.Duration(rand.Float64()*0.5*float64(delay)) - time.Duration(0.25*float64(delay))
	delay += jitter

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./extensions/retry -v
```

Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add extensions/retry/
git commit -m "feat(extensions/retry): add exponential backoff retry wrapper"
```

---

## Task 3: Fallback Extension

**Files:**
- Create: `extensions/fallback/model.go`
- Create: `extensions/fallback/model_test.go`

**Prerequisites:** Task 1 complete (`extensions/errors/`).

- [ ] **Step 1: Write the failing test**

Create `extensions/fallback/model_test.go`:

```go
package fallback

import (
	"context"
	"errors"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockModel struct {
	name      string
	fail      bool
	failStream bool
	calls     int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	if m.fail {
		return nil, &core.ProviderError{Status: 500, Message: "fail"}
	}
	return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: m.name}}}}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	m.calls++
	if m.failStream {
		return nil, &core.ProviderError{Status: 500, Message: "stream fail"}
	}
	return func(yield func(*core.StreamPart, error) bool) {
		yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: m.name}, nil)
	}, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) Provider() string { return m.name }
func (m *mockModel) Model() string    { return m.name }

func TestGenerateFirstSucceeds(t *testing.T) {
	m1 := &mockModel{name: "primary"}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	resp, err := fb.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m1.calls != 1 {
		t.Errorf("m1 calls: got %d, want 1", m1.calls)
	}
	if m2.calls != 0 {
		t.Errorf("m2 calls: got %d, want 0", m2.calls)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "primary" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
}

func TestGenerateFallback(t *testing.T) {
	m1 := &mockModel{name: "primary", fail: true}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	resp, err := fb.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m1.calls != 1 {
		t.Errorf("m1 calls: got %d, want 1", m1.calls)
	}
	if m2.calls != 1 {
		t.Errorf("m2 calls: got %d, want 1", m2.calls)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "backup" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
}

func TestGenerateAllFail(t *testing.T) {
	m1 := &mockModel{name: "primary", fail: true}
	m2 := &mockModel{name: "backup", fail: true}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	_, err := fb.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	if m1.calls != 1 || m2.calls != 1 {
		t.Errorf("calls: m1=%d m2=%d, want 1 each", m1.calls, m2.calls)
	}
}

func TestStreamFallback(t *testing.T) {
	m1 := &mockModel{name: "primary", failStream: true}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	stream, err := fb.Stream(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}

	var got string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeTextDelta {
			got += part.TextDelta
		}
	}
	if got != "backup" {
		t.Errorf("got %q, want backup", got)
	}
	if m1.calls != 1 || m2.calls != 1 {
		t.Errorf("calls: m1=%d m2=%d, want 1 each", m1.calls, m2.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./extensions/fallback -v
```

Expected: FAIL — `Model`, `Candidates` undefined.

- [ ] **Step 3: Write implementation**

Create `extensions/fallback/model.go`:

```go
package fallback

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Model tries multiple LanguageModel candidates in order until one succeeds.
type Model struct {
	Candidates []core.LanguageModel
}

func (m *Model) Provider() string {
	if len(m.Candidates) > 0 {
		return m.Candidates[0].Provider()
	}
	return "fallback"
}

func (m *Model) Model() string {
	if len(m.Candidates) > 0 {
		return m.Candidates[0].Model()
	}
	return ""
}

func (m *Model) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	var lastErr error
	for _, candidate := range m.Candidates {
		resp, err := candidate.Generate(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (m *Model) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	var lastErr error
	for _, candidate := range m.Candidates {
		stream, err := candidate.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (m *Model) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	var lastErr error
	for _, candidate := range m.Candidates {
		resp, err := candidate.GenerateObject(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (m *Model) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	var lastErr error
	for _, candidate := range m.Candidates {
		stream, err := candidate.StreamObject(ctx, req)
		if err == nil {
			return stream, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./extensions/fallback -v
```

Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add extensions/fallback/
git commit -m "feat(extensions/fallback): add multi-candidate fallback wrapper"
```

---

## Task 4: Embed Extension

**Files:**
- Create: `extensions/embed/provider.go`
- Create: `extensions/embed/provider_test.go`

**Prerequisites:** `core/` package built.

- [ ] **Step 1: Write the failing test**

Create `extensions/embed/provider_test.go`:

```go
package embed

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockEmbedProvider struct{}

func (m *mockEmbedProvider) Name() string { return "mock-embed" }

func (m *mockEmbedProvider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, nil
}

func (m *mockEmbedProvider) EmbeddingModel(ctx context.Context, modelID string) (EmbeddingModel, error) {
	return &mockEmbedModel{}, nil
}

type mockEmbedModel struct{}

func (m *mockEmbedModel) Embed(ctx context.Context, texts []string) (*EmbeddingResponse, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = []float32{float32(i) + 0.1, float32(i) + 0.2}
	}
	return &EmbeddingResponse{
		Embeddings: embeddings,
		Usage:      core.Usage{PromptTokens: len(texts) * 10, TotalTokens: len(texts) * 10},
	}, nil
}

func TestEmbed(t *testing.T) {
	p := &mockEmbedProvider{}
	model, err := p.EmbeddingModel(context.Background(), "text-embedding-3-small")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := model.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) != 2 {
		t.Errorf("embedding[0] len: got %d, want 2", len(resp.Embeddings[0]))
	}
	if resp.Usage.TotalTokens != 20 {
		t.Errorf("usage total: got %d, want 20", resp.Usage.TotalTokens)
	}
}

func TestProviderImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*mockEmbedProvider)(nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./extensions/embed -v
```

Expected: FAIL — `EmbeddingModel`, `EmbeddingResponse`, `Provider` undefined.

- [ ] **Step 3: Write implementation**

Create `extensions/embed/provider.go`:

```go
package embed

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Provider extends core.Provider with embedding capabilities.
type Provider interface {
	core.Provider
	EmbeddingModel(ctx context.Context, modelID string) (EmbeddingModel, error)
}

// EmbeddingModel generates vector embeddings for text.
type EmbeddingModel interface {
	Embed(ctx context.Context, texts []string) (*EmbeddingResponse, error)
}

// EmbeddingResponse holds embeddings and token usage.
type EmbeddingResponse struct {
	Embeddings [][]float32
	Usage      core.Usage
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./extensions/embed -v
```

Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
git add extensions/embed/
git commit -m "feat(extensions/embed): add embedding interfaces"
```

---

## Task 5: Final Verification

- [ ] **Step 1: Full build**

```bash
go build ./...
```

Expected: PASS (no output).

- [ ] **Step 2: Full test**

```bash
go test ./... -v
```

Expected: All tests PASS across core, providers, and extensions.

- [ ] **Step 3: Vet**

```bash
go vet ./...
```

Expected: PASS (no output).

- [ ] **Step 4: Commit go.sum if changed**

```bash
git add -A
git commit -m "chore: verify Phase 2 extensions build and test clean" || echo "nothing to commit"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ Error classifier with 7 Kinds — Task 1
- ✅ Retry with exponential backoff + jitter — Task 2
- ✅ Fallback with sequential candidate list — Task 3
- ✅ Embed Provider + EmbeddingModel interfaces — Task 4
- ✅ All wrappers implement `core.LanguageModel` — Tasks 2, 3
- ✅ Composition example (`retry` → `fallback`) supported by design — Tasks 2, 3

**2. Placeholder scan:** No TBD, TODO, or vague steps found. Every step contains complete code.

**3. Type consistency:**
- `core.LanguageModel` interface used consistently across retry and fallback
- `core.ProviderError` referenced from `core/errors.go` (Phase 1)
- `core.StreamResponse` = `iter.Seq2[*StreamPart, error]` used in retry/fallback Stream methods
- `core.Usage` referenced in embed.EmbeddingResponse

---

## Phase 2 完成标准

- [ ] `extensions/errors/` 编译通过，测试通过
- [ ] `extensions/retry/` 编译通过，测试通过
- [ ] `extensions/fallback/` 编译通过，测试通过
- [ ] `extensions/embed/` 编译通过，测试通过
- [ ] `go build ./...` 和 `go test ./...` 全绿
- [ ] `go vet ./...` 无警告
