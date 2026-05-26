# Native/Local Embedding Provider Implementation Plan

## Scope

Implement a single `providers/native/` package that runs BERT-based embedding models locally using `github.com/nlpodyssey/cybertron` (pure Go, no CGO). This is Batch 2 of the embedding provider work.

## Technical Decisions

| Decision | Rationale |
|----------|-----------|
| **Cybertron** (`nlpodyssey/cybertron`) | Pure Go, no CGO. Supports BERT/DeBERTa models compatible with target models (all-MiniLM-L6-v2, nomic-embed-text-v1, multilingual-e5-small). |
| **Library mode only** | Confirmed via `go mod tidy` — server deps (buf, grpc, docker) pruned automatically. |
| **File layout** | Follows provider convention: `provider.go`, `model.go`, `embed.go`, `doc.go`, `provider_test.go` |
| **Model config** | Via `WithModelDir(string)` + `WithModelName(string)` options. |
| **Embeddings only** | `LanguageModel` methods return error. `EmbeddingModel` methods create Cybertron task on first call and cache it. |
| **Batching** | Pass through to Cybertron's internal batching. No custom batch limit (models handle their own). |
| **Concurrency** | Thread-safe: use `sync.Once` for model loading. |

## Task Breakdown

| # | Task | Est. Lines | Test |
|---|------|-----------|------|
| 1 | Create `providers/native/doc.go` | 10 | - |
| 2 | Create `providers/native/provider.go` — `Provider`, `New()`, `Name()`, `Models()`, `LanguageModel()`, `EmbeddingModel()` | 70 | `TestProvider` |
| 3 | Create `providers/native/embed.go` — `EmbeddingModel`, `Embed()`, `EmbedQuery()`, model loading | 90 | `TestProvider_EmbeddingModel` |
| 4 | Create `providers/native/model.go` — `LanguageModel` (error stubs) | 30 | `TestProvider_LanguageModel` |
| 5 | Create `providers/native/provider_test.go` — unit tests | 80 | `go test ./...` |
| 6 | Verify `go build ./...` and `go test ./...` | - | All pass |

## Acceptance Criteria

- `go build ./...` passes with no new warnings
- `go test ./...` passes for all packages
- New `native` provider tested with: model loading, embedding generation (mocked or real if model available), error cases
- Code follows project conventions (same as Batch 1 providers)
