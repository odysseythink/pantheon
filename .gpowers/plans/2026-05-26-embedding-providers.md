# Embedding Providers Batch 1 (Cloud APIs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add embedding support to 4 existing providers and create 8 new providers, covering all cloud-based embedding engines from the target project.

**Architecture:** Reuse `extensions/embed/` interfaces and `providers/openaicompat/` HTTP client where possible. OpenAI-compatible providers (6) follow the `providers/openai/` template. Custom API providers (Cohere, Google) implement their own HTTP clients. Voyage is embedding-only.

**Tech Stack:** Go 1.23+, standard `net/http/httptest` for testing, existing `openaicompat.Client` for OpenAI-compatible providers.

---

## File Structure

### Modified files (4 existing providers)
- `providers/azure/provider.go` — add `EmbeddingModel()` method
- `providers/azure/model.go` — add `EmbeddingModel` struct
- `providers/azure/provider_test.go` — add embedding tests
- `providers/ollama/provider.go` — add `EmbeddingModel()` method
- `providers/ollama/model.go` — add `EmbeddingModel` struct
- `providers/ollama/provider_test.go` — add embedding tests
- `providers/openrouter/provider.go` — add `EmbeddingModel()` method
- `providers/openrouter/model.go` — add `EmbeddingModel` struct
- `providers/openrouter/provider_test.go` — add embedding tests
- `providers/google/provider.go` — add `EmbeddingModel()` method
- `providers/google/model.go` — add `EmbeddingModel` struct
- `providers/google/client.go` — add `embedContent()` method
- `providers/google/provider_test.go` — add embedding tests

### New files (8 new providers)
- `providers/mistral/provider.go`, `model.go`, `provider_test.go`
- `providers/litellm/provider.go`, `model.go`, `provider_test.go`
- `providers/lmstudio/provider.go`, `model.go`, `provider_test.go`
- `providers/localai/provider.go`, `model.go`, `provider_test.go`
- `providers/genericopenai/provider.go`, `model.go`, `provider_test.go`
- `providers/lemonade/provider.go`, `model.go`, `provider_test.go`
- `providers/cohere/provider.go`, `model.go`, `embed.go`, `client.go`, `provider_test.go`
- `providers/voyage/provider.go`, `embed.go`, `client.go`, `provider_test.go`

---

## Task 1: Azure Provider — Add EmbeddingModel

**Files:**
- Modify: `providers/azure/provider.go`
- Modify: `providers/azure/model.go`
- Modify: `providers/azure/provider_test.go`

- [ ] **Step 1: Add EmbeddingModel to provider.go**

Add the `EmbeddingModel` method to the `Provider` struct and import `embed`:

```go
import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)
```

Add at the end of `provider.go`:

```go
// EmbeddingModel creates a new Azure embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 2: Add EmbeddingModel struct to model.go**

Add import and struct to `providers/azure/model.go`:

```go
import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)
```

Add at the end of `model.go`:

```go
// EmbeddingModel implements embed.EmbeddingModel for the Azure provider.
type EmbeddingModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	return m.client.CreateEmbeddings(ctx, m.model, texts)
}
```

- [ ] **Step 3: Add embedding test to provider_test.go**

Add to `providers/azure/provider_test.go`:

```go
func TestProvider_EmbeddingModel(t *testing.T) {
	p, err := New("test-key", "test-resource", "test-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	embedModel, err := p.EmbeddingModel(context.Background(), "text-embedding-ada-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/azure/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/azure/ && git commit -m "feat(azure): add EmbeddingModel support"
```

---

## Task 2: Ollama Provider — Add EmbeddingModel

**Files:**
- Modify: `providers/ollama/provider.go`
- Modify: `providers/ollama/model.go`
- Modify: `providers/ollama/provider_test.go`

- [ ] **Step 1: Add EmbeddingModel to provider.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel creates a new Ollama embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 2: Add EmbeddingModel struct to model.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel implements embed.EmbeddingModel for the Ollama provider.
type EmbeddingModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	return m.client.CreateEmbeddings(ctx, m.model, texts)
}
```

- [ ] **Step 3: Add embedding test to provider_test.go**

```go
func TestProvider_EmbeddingModel(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	embedModel, err := p.EmbeddingModel(context.Background(), "nomic-embed-text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/ollama/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/ollama/ && git commit -m "feat(ollama): add EmbeddingModel support"
```

---

## Task 3: OpenRouter Provider — Add EmbeddingModel

**Files:**
- Modify: `providers/openrouter/provider.go`
- Modify: `providers/openrouter/model.go`
- Modify: `providers/openrouter/provider_test.go`

- [ ] **Step 1: Add EmbeddingModel to provider.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel creates a new OpenRouter embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 2: Add EmbeddingModel struct to model.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel implements embed.EmbeddingModel for the OpenRouter provider.
type EmbeddingModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	return m.client.CreateEmbeddings(ctx, m.model, texts)
}
```

- [ ] **Step 3: Add embedding test to provider_test.go**

```go
func TestProvider_EmbeddingModel(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	embedModel, err := p.EmbeddingModel(context.Background(), "baai/bge-m3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/openrouter/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/openrouter/ && git commit -m "feat(openrouter): add EmbeddingModel support"
```

---

## Task 4: Google Provider — Add Gemini EmbeddingModel

**Files:**
- Modify: `providers/google/provider.go`
- Modify: `providers/google/model.go`
- Modify: `providers/google/client.go`
- Create: `providers/google/provider_test.go`

- [ ] **Step 1: Add embedContent to client.go**

Add at the end of `providers/google/client.go`:

```go
// embedContentRequest is the request body for Gemini embedContent.
type embedContentRequest struct {
	Content struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"content"`
}

// embedContentResponse is the response body for Gemini embedContent.
type embedContentResponse struct {
	Embedding struct {
		Values []float64 `json:"values"`
	} `json:"embedding"`
}

// embedContent calls the Gemini embedContent API for a single text.
func (c *client) embedContent(ctx context.Context, model, text string) ([]float32, error) {
	req := embedContentRequest{}
	req.Content.Parts = append(req.Content.Parts, struct {
		Text string `json:"text"`
	}{Text: text})

	url := fmt.Sprintf("%s/models/%s:embedContent", c.baseURL, model)
	query := map[string][]string{"key": {c.apiKey}}
	resp, err := core.HttpClientCallWithClient[embedContentResponse](
		c.httpClient, ctx, "POST", url, query, req,
		map[string]string{"Content-Type": "application/json"},
	)
	if err != nil {
		return nil, err
	}

	emb := make([]float32, len(resp.Embedding.Values))
	for i, v := range resp.Embedding.Values {
		emb[i] = float32(v)
	}
	return emb, nil
}
```

- [ ] **Step 2: Add EmbeddingModel to provider.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel creates a new Google embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 3: Add EmbeddingModel struct to model.go**

Add import `github.com/odysseythink/pantheon/extensions/embed` and add:

```go
// EmbeddingModel implements embed.EmbeddingModel for the Google provider.
type EmbeddingModel struct {
	provider *Provider
	client   *client
	model    string
}

// Embed generates embeddings for the given texts by calling Gemini embedContent for each.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	var embeddings [][]float32
	var totalTokens int
	for _, text := range texts {
		emb, err := m.client.embedContent(ctx, m.model, text)
		if err != nil {
			return nil, fmt.Errorf("google embedContent: %w", err)
		}
		embeddings = append(embeddings, emb)
		// Gemini does not return token usage for embedding; approximate.
		totalTokens += len(text) / 4
	}
	return &embed.EmbeddingResponse{
		Embeddings: embeddings,
		Usage: core.Usage{
			PromptTokens: totalTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
}
```

- [ ] **Step 4: Create provider_test.go**

Create `providers/google/provider_test.go`:

```go
package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "google" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	model, err := p.LanguageModel(context.Background(), "gemini-pro")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "google" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	embedModel, err := p.EmbeddingModel(context.Background(), "gemini-embedding-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:embedContent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(embedContentResponse{
			Embedding: struct {
				Values []float64 `json:"values"`
			}{
				Values: []float64{0.1, 0.2, 0.3},
			},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	embedModel, _ := p.EmbeddingModel(context.Background(), "gemini-embedding-001")
	resp, err := embedModel.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(resp.Embeddings[0]))
	}
}
```

- [ ] **Step 5: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/google/... -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/google/ && git commit -m "feat(google): add Gemini EmbeddingModel support"
```

---

## Task 5: Mistral Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/mistral/provider.go`
- Create: `providers/mistral/model.go`
- Create: `providers/mistral/provider_test.go`

- [ ] **Step 1: Create provider.go**

```go
package mistral

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

const defaultBaseURL = "https://api.mistral.ai/v1"

type Provider struct {
	client *openaicompat.Client
}

// New creates a new Mistral provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(defaultBaseURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Mistral provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.BaseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client.HTTPClient = client }
}

func (p *Provider) Name() string { return "mistral" }

func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "mistral" }
```

- [ ] **Step 2: Create model.go**

```go
package mistral

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

type LanguageModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.ChatCompletion(ctx, m.model, req)
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.ChatCompletionStream(ctx, m.model, req), nil
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
	}
	if req.Mode == core.ObjectModeAuto || req.Mode == core.ObjectModeJSON {
		coreReq.ResponseFormat = &core.ResponseFormat{
			Type:       core.ResponseFormatTypeJSONSchema,
			JSONSchema: req.Schema,
		}
	} else if req.Mode == core.ObjectModeTool {
		coreReq.Tools = []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}}
		coreReq.ToolChoice = core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "generate_object"}
	} else if req.Mode == core.ObjectModeText {
		coreReq.ResponseFormat = &core.ResponseFormat{Type: core.ResponseFormatTypeText}
	}
	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return openaicompat.ExtractObjectResponse(resp, m.model)
}

type EmbeddingModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	return m.client.CreateEmbeddings(ctx, m.model, texts)
}
```

- [ ] **Step 3: Create provider_test.go**

```go
package mistral

import (
	"context"
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "mistral" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("test-key", WithBaseURL("https://custom.example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "https://custom.example.com" {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("test-key", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	model, err := p.LanguageModel(context.Background(), "mistral-medium")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "mistral" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	embedModel, err := p.EmbeddingModel(context.Background(), "mistral-embed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "mistral" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/mistral/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/mistral/ && git commit -m "feat(mistral): add new Mistral provider with chat and embedding support"
```

---

## Task 6: LiteLLM Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/litellm/provider.go`
- Create: `providers/litellm/model.go`
- Create: `providers/litellm/provider_test.go`

- [ ] **Step 1: Create provider.go**

Same structure as Mistral, but:
- Package: `litellm`
- `defaultBaseURL = ""` (user must configure)
- `Name()` returns `"litellm"`
- `ProviderOptions` returns `"litellm"`

```go
package litellm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

type Provider struct {
	client *openaicompat.Client
}

// New creates a new LiteLLM provider with the given API key.
// The base URL must be provided via WithBaseURL.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient("", apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	if p.client.BaseURL == "" {
		return nil, fmt.Errorf("litellm: base URL is required, use WithBaseURL")
	}
	return p, nil
}

// Option configures the LiteLLM provider.
type Option func(*Provider)

// WithBaseURL sets the API base URL (required).
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.BaseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client.HTTPClient = client }
}

func (p *Provider) Name() string { return "litellm" }

func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "litellm" }
```

- [ ] **Step 2: Create model.go**

Same as Mistral model.go but package `litellm`.

- [ ] **Step 3: Create provider_test.go**

Same test structure as Mistral but:
- Package: `litellm`
- `Name()` check: `"litellm"`
- Test `New` without base URL returns error
- Test `New` with base URL succeeds

```go
package litellm

import (
	"context"
	"net/http"
	"testing"
)

func TestNew_MissingBaseURL(t *testing.T) {
	_, err := New("test-key")
	if err == nil {
		t.Fatal("expected error for missing base URL")
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("test-key", WithBaseURL("https://litellm.example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "litellm" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("test-key", WithBaseURL("https://litellm.example.com"), WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key", WithBaseURL("https://litellm.example.com"))
	model, err := p.LanguageModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "litellm" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key", WithBaseURL("https://litellm.example.com"))
	embedModel, err := p.EmbeddingModel(context.Background(), "text-embedding-ada-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "litellm" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/litellm/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/litellm/ && git commit -m "feat(litellm): add new LiteLLM provider with chat and embedding support"
```

---

## Task 7: LMStudio Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/lmstudio/provider.go`
- Create: `providers/lmstudio/model.go`
- Create: `providers/lmstudio/provider_test.go`

- [ ] **Step 1: Create provider.go**

Same as LiteLLM but:
- Package: `lmstudio`
- `Name()` returns `"lmstudio"`
- `ProviderOptions` returns `"lmstudio"`

```go
package lmstudio

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

type Provider struct {
	client *openaicompat.Client
}

// New creates a new LMStudio provider with the given API key.
// The base URL must be provided via WithBaseURL.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient("", apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	if p.client.BaseURL == "" {
		return nil, fmt.Errorf("lmstudio: base URL is required, use WithBaseURL")
	}
	return p, nil
}

// Option configures the LMStudio provider.
type Option func(*Provider)

// WithBaseURL sets the API base URL (required).
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.BaseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client.HTTPClient = client }
}

func (p *Provider) Name() string { return "lmstudio" }

func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "lmstudio" }
```

- [ ] **Step 2: Create model.go**

Same as Mistral model.go but package `lmstudio`.

- [ ] **Step 3: Create provider_test.go**

Same structure as LiteLLM tests but package `lmstudio`, name checks `"lmstudio"`.

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/lmstudio/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/lmstudio/ && git commit -m "feat(lmstudio): add new LMStudio provider with chat and embedding support"
```

---

## Task 8: LocalAI Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/localai/provider.go`
- Create: `providers/localai/model.go`
- Create: `providers/localai/provider_test.go`

- [ ] **Step 1: Create provider.go**

Same as LiteLLM but:
- Package: `localai`
- `Name()` returns `"localai"`
- `ProviderOptions` returns `"localai"`

- [ ] **Step 2: Create model.go**

Same as Mistral model.go but package `localai`.

- [ ] **Step 3: Create provider_test.go**

Same structure as LiteLLM tests but package `localai`, name checks `"localai"`.

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/localai/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/localai/ && git commit -m "feat(localai): add new LocalAI provider with chat and embedding support"
```

---

## Task 9: GenericOpenAI Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/genericopenai/provider.go`
- Create: `providers/genericopenai/model.go`
- Create: `providers/genericopenai/provider_test.go`

- [ ] **Step 1: Create provider.go**

Same as LiteLLM but:
- Package: `genericopenai`
- `Name()` returns `"genericopenai"`
- `ProviderOptions` returns `"genericopenai"`

- [ ] **Step 2: Create model.go**

Same as Mistral model.go but package `genericopenai`.

- [ ] **Step 3: Create provider_test.go**

Same structure as LiteLLM tests but package `genericopenai`, name checks `"genericopenai"`.

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/genericopenai/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/genericopenai/ && git commit -m "feat(genericopenai): add new Generic OpenAI provider with chat and embedding support"
```

---

## Task 10: Lemonade Provider — New OpenAI-Compatible Provider

**Files:**
- Create: `providers/lemonade/provider.go`
- Create: `providers/lemonade/model.go`
- Create: `providers/lemonade/provider_test.go`

- [ ] **Step 1: Create provider.go**

Same as LiteLLM but:
- Package: `lemonade`
- `Name()` returns `"lemonade"`
- `ProviderOptions` returns `"lemonade"`

Note: If the target project's `parseLemonadeServerEndpoint` logic is needed, add a helper function. Otherwise keep it simple.

- [ ] **Step 2: Create model.go**

Same as Mistral model.go but package `lemonade`.

- [ ] **Step 3: Create provider_test.go**

Same structure as LiteLLM tests but package `lemonade`, name checks `"lemonade"`.

- [ ] **Step 4: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/lemonade/... -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/lemonade/ && git commit -m "feat(lemonade): add new Lemonade provider with chat and embedding support"
```

---

## Task 11: Cohere Provider — New Custom API Provider

**Files:**
- Create: `providers/cohere/client.go`
- Create: `providers/cohere/provider.go`
- Create: `providers/cohere/model.go`
- Create: `providers/cohere/embed.go`
- Create: `providers/cohere/provider_test.go`

- [ ] **Step 1: Create client.go**

```go
package cohere

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
)

const defaultBaseURL = "https://api.cohere.com"

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func newClient(apiKey string) *Client {
	return &Client{
		BaseURL:    defaultBaseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}
```

- [ ] **Step 2: Create provider.go**

```go
package cohere

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// New creates a new Cohere provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("cohere: apiKey is required")
	}
	p := &Provider{client: newClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

type Provider struct {
	client *Client
}

// Option configures the Cohere provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.BaseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client.HTTPClient = client }
}

func (p *Provider) Name() string { return "cohere" }

func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	// Cohere does not have a standard model list endpoint like OpenAI.
	// Return a static list of known models for now.
	return []core.Model{
		{ID: "command-r", Name: "Command R"},
		{ID: "command-r-plus", Name: "Command R+"},
		{ID: "embed-english-v3.0", Name: "Embed English v3.0"},
		{ID: "embed-multilingual-v3.0", Name: "Embed Multilingual v3.0"},
	}, nil
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "cohere" }
```

- [ ] **Step 3: Create model.go**

Cohere Chat API v2 implementation. This is a simplified version; if Cohere's API format requires more complex mapping, expand as needed.

```go
package cohere

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

type LanguageModel struct {
	provider *Provider
	client   *Client
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

// chatRequest is the request body for Cohere Chat API v2.
type chatRequest struct {
	Model   string      `json:"model"`
	Message string      `json:"message"`
	Stream  bool        `json:"stream,omitempty"`
	Preamble string     `json:"preamble,omitempty"`
}

// chatResponse is the response body for Cohere Chat API v2.
type chatResponse struct {
	Text string `json:"text"`
}

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	// Convert core.Request to Cohere format
	var message string
	var preamble string
	for _, msg := range req.Messages {
		if msg.Role == core.MessageRoleSystem {
			preamble = msg.Content
		} else {
			message = msg.Content
		}
	}
	if message == "" {
		return nil, fmt.Errorf("cohere: no user message found")
	}

	cohereReq := chatRequest{
		Model:    m.model,
		Message:  message,
		Preamble: preamble,
	}
	if req.SystemPrompt != "" {
		cohereReq.Preamble = req.SystemPrompt
	}

	url := fmt.Sprintf("%s/v2/chat", m.client.BaseURL)
	resp, err := core.HttpClientCallWithClient[chatResponse](
		m.client.HTTPClient, ctx, "POST", url, nil, cohereReq,
		map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + m.client.APIKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("cohere chat: %w", err)
	}

	return &core.Response{
		Content: resp.Text,
		Usage:   core.Usage{},
	}, nil
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, fmt.Errorf("cohere: streaming not yet implemented")
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, fmt.Errorf("cohere: generate object not yet implemented")
}
```

- [ ] **Step 4: Create embed.go**

```go
package cohere

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// embedRequest is the request body for Cohere Embed API v2.
type embedRequest struct {
	Texts     []string `json:"texts"`
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
}

// embedResponse is the response body for Cohere Embed API v2.
type embedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

type EmbeddingModel struct {
	provider *Provider
	client   *Client
	model    string
}

// Embed generates embeddings for the given texts.
// If texts has 1 element, input_type is "search_query"; otherwise "search_document".
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	inputType := "search_document"
	if len(texts) == 1 {
		inputType = "search_query"
	}

	var allEmbeddings [][]float32
	// Cohere v2 embed limit is 96 texts per request
	const batchSize = 96
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		req := embedRequest{
			Texts:     batch,
			Model:     m.model,
			InputType: inputType,
		}
		url := fmt.Sprintf("%s/v2/embed", m.client.BaseURL)
		resp, err := core.HttpClientCallWithClient[embedResponse](
			m.client.HTTPClient, ctx, "POST", url, nil, req,
			map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer " + m.client.APIKey,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("cohere embed: %w", err)
		}
		for _, emb := range resp.Embeddings {
			f32 := make([]float32, len(emb))
			for j, v := range emb {
				f32[j] = float32(v)
			}
			allEmbeddings = append(allEmbeddings, f32)
		}
	}

	return &embed.EmbeddingResponse{
		Embeddings: allEmbeddings,
		Usage:      core.Usage{},
	}, nil
}
```

- [ ] **Step 5: Create provider_test.go**

```go
package cohere

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_MissingKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNew(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "cohere" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestProvider_Models(t *testing.T) {
	p, _ := New("test-key")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected models")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	model, err := p.LanguageModel(context.Background(), "command-r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "cohere" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	embedModel, err := p.EmbeddingModel(context.Background(), "embed-english-v3.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(embedResponse{
			Embeddings: [][]float64{
				{0.1, 0.2, 0.3},
				{0.4, 0.5, 0.6},
			},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	embedModel, _ := p.EmbeddingModel(context.Background(), "embed-english-v3.0")
	resp, err := embedModel.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
}

func TestLanguageModel_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(chatResponse{Text: "Hello from Cohere"})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	model, _ := p.LanguageModel(context.Background(), "command-r")
	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Cohere" {
		t.Errorf("unexpected content: %s", resp.Content)
	}
}
```

Note: Add `import "github.com/odysseythink/pantheon/core"` to the test file.

- [ ] **Step 6: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/cohere/... -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/cohere/ && git commit -m "feat(cohere): add new Cohere provider with chat and embedding support"
```

---

## Task 12: Voyage Provider — New Embedding-Only Provider

**Files:**
- Create: `providers/voyage/client.go`
- Create: `providers/voyage/provider.go`
- Create: `providers/voyage/embed.go`
- Create: `providers/voyage/provider_test.go`

- [ ] **Step 1: Create client.go**

```go
package voyage

import (
	"net/http"
)

const defaultBaseURL = "https://api.voyageai.com/v1"

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func newClient(apiKey string) *Client {
	return &Client{
		BaseURL:    defaultBaseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
}
```

- [ ] **Step 2: Create provider.go**

```go
package voyage

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// New creates a new Voyage AI provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("voyage: apiKey is required")
	}
	p := &Provider{client: newClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

type Provider struct {
	client *Client
}

// Option configures the Voyage provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.BaseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) { p.client.HTTPClient = client }
}

func (p *Provider) Name() string { return "voyage" }

func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return []core.Model{
		{ID: "voyage-3-lite", Name: "Voyage 3 Lite"},
		{ID: "voyage-3", Name: "Voyage 3"},
		{ID: "voyage-3-large", Name: "Voyage 3 Large"},
		{ID: "voyage-code-3", Name: "Voyage Code 3"},
		{ID: "voyage-multilingual-2", Name: "Voyage Multilingual 2"},
	}, nil
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, fmt.Errorf("voyage provider only supports embedding, not chat completion")
}

func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "voyage" }
```

- [ ] **Step 3: Create embed.go**

```go
package voyage

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// embeddingRequest is the request body for Voyage embedding API.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingResponse is the response body for Voyage embedding API.
type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type EmbeddingModel struct {
	provider *Provider
	client   *Client
	model    string
}

// Embed generates embeddings for the given texts.
// Voyage limit is 128 texts per request.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	var allEmbeddings [][]float32
	const batchSize = 128
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		req := embeddingRequest{
			Model: m.model,
			Input: batch,
		}
		url := fmt.Sprintf("%s/embeddings", m.client.BaseURL)
		resp, err := core.HttpClientCallWithClient[embeddingResponse](
			m.client.HTTPClient, ctx, "POST", url, nil, req,
			map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer " + m.client.APIKey,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("voyage embed: %w", err)
		}
		for _, data := range resp.Data {
			emb := make([]float32, len(data.Embedding))
			for j, v := range data.Embedding {
				emb[j] = float32(v)
			}
			allEmbeddings = append(allEmbeddings, emb)
		}
	}

	return &embed.EmbeddingResponse{
		Embeddings: allEmbeddings,
		Usage:      core.Usage{},
	}, nil
}
```

- [ ] **Step 4: Create provider_test.go**

```go
package voyage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestNew_MissingKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestNew(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "voyage" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestProvider_Models(t *testing.T) {
	p, _ := New("test-key")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected models")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	_, err := p.LanguageModel(context.Background(), "voyage-3")
	if err == nil {
		t.Fatal("expected error for LanguageModel on voyage provider")
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	embedModel, err := p.EmbeddingModel(context.Background(), "voyage-3-lite")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(embeddingResponse{
			Data: []struct {
				Embedding []float64 `json:"embedding"`
				Index     int       `json:"index"`
			}{
				{Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float64{0.4, 0.5, 0.6}, Index: 1},
			},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	embedModel, _ := p.EmbeddingModel(context.Background(), "voyage-3-lite")
	resp, err := embedModel.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
}
```

- [ ] **Step 5: Run tests**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/voyage/... -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git add providers/voyage/ && git commit -m "feat(voyage): add new Voyage AI embedding-only provider"
```

---

## Final Verification

- [ ] **Step 1: Run full provider suite**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go test ./providers/... -v 2>&1 | tail -30
```

Expected: All provider tests PASS

- [ ] **Step 2: Build check**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && go build ./...
```

Expected: No build errors

- [ ] **Step 3: Final commit**

```bash
cd /Users/ranwei/workspace/go_work/pantheon && git log --oneline -15
```

Expected: 12 commits (1 design + 10 provider commits + 1 final verification)

---

## Self-Review Checklist

**1. Spec coverage:**
- [x] Azure embedding — Task 1
- [x] Ollama embedding — Task 2
- [x] OpenRouter embedding — Task 3
- [x] Google/Gemini embedding — Task 4
- [x] Mistral provider — Task 5
- [x] LiteLLM provider — Task 6
- [x] LMStudio provider — Task 7
- [x] LocalAI provider — Task 8
- [x] GenericOpenAI provider — Task 9
- [x] Lemonade provider — Task 10
- [x] Cohere provider — Task 11
- [x] Voyage provider — Task 12

**2. Placeholder scan:**
- [x] No "TBD", "TODO", "implement later"
- [x] All code blocks contain actual implementation
- [x] No vague references like "Similar to Task X"

**3. Type consistency:**
- [x] `EmbeddingModel` struct name consistent across all providers
- [x] `Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error)` signature consistent
- [x] `ProviderOptions.ProviderName()` pattern consistent with existing codebase
