# Pantheon

Pantheon is a unified AI SDK for Go. It provides a single, provider-agnostic interface for calling LLMs — including chat completion, streaming, tool use, structured output, and embeddings — with a composable extension system for retries, fallbacks, and agents.

## Features

- **Unified Core API** — One `LanguageModel` interface for every provider. Switch from OpenAI to Anthropic (or any other) by changing a single constructor call.
- **Streaming First** — Native `iter.Seq2` streaming with text deltas, reasoning deltas, tool calls, and usage events.
- **Tool Use** — Register Go functions as tools and let the model invoke them. The Agent layer handles the full loop automatically.
- **Structured Output** — Generate typed objects via JSON Schema or tool-mode extraction with `GenerateObject`.
- **Resilient by Design** — Composable retry, fallback, and error-classification extensions. No magic, just wrapper structs.
- **Multi-Provider** — Core providers ship with the main module; extra providers live in a separate, independently versioned module.
- **Agent Toolkit** — `agent/budget` iteration counter, `agent/trajectory` JSONL writer, `tool` rich registry with panic recovery and truncation, `extensions/toolselector` keyword-based filtering, `extensions/delegate` subagent dispatch.
- **Observability** — `observability/tracing` OTLP HTTP exporter and `observability/metrics` Prometheus-format registry.
- **Security** — `security/mcpoauth` OAuth 2.1 client, `security/osv` vulnerability lookup, `security/urlsafety` allow/deny host policy.
- **Skills** — `extensions/skills` markdown skill parser, loader, registry, retriever (with embedding rerank), injector, and LLM-based evolver.
- **Judge** — `extensions/judge` LLM-as-judge for conversation quality scoring and skill draft extraction.
- **Utilities** — `utils/pricing` catwalk-backed cost calculator, `utils/redact` secret-pattern scrubber, `extensions/embed` cosine similarity and reranking.

## Installation

```bash
go get github.com/odysseythink/pantheon
```

For extra providers (DeepSeek, Qwen, etc.):

```bash
go get github.com/odysseythink/pantheon/providers-extra
```

## Quick Start

### Basic Generation

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/odysseythink/pantheon/core"
    "github.com/odysseythink/pantheon/providers/openai"
)

func main() {
    ctx := context.Background()

    provider, err := openai.New("sk-...")
    if err != nil {
        log.Fatal(err)
    }

    model, err := provider.LanguageModel(ctx, "gpt-4o")
    if err != nil {
        log.Fatal(err)
    }

    resp, err := model.Generate(ctx, &core.Request{
        Messages: []core.Message{
            {Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{
                core.TextPart{Text: "What is Go?"},
            }},
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(resp.Message.Content[0].(core.TextPart).Text)
}
```

### Streaming

```go
stream, err := model.Stream(ctx, &core.Request{...})
if err != nil {
    log.Fatal(err)
}

for part, err := range stream {
    if err != nil {
        log.Fatal(err)
    }
    switch part.Type {
    case core.StreamPartTypeTextDelta:
        fmt.Print(part.TextDelta)
    case core.StreamPartTypeFinish:
        fmt.Println("\n[done:", part.FinishReason, "]")
    }
}
```

### Tool Use with Agent

```go
import "github.com/odysseythink/pantheon/agent"

a := agent.New(model, agent.WithMaxSteps(10))

a.RegisterTool("getWeather", func(city string) string {
    return "Sunny, 24°C"
})

result, err := a.Run(ctx, &agent.Request{
    Messages: []core.Message{
        {Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{
            core.TextPart{Text: "What's the weather in Tokyo?"},
        }},
    },
    Tools: []core.ToolDefinition{{
        Name:        "getWeather",
        Description: "Get current weather for a city",
        Parameters:  core.GenerateSchema(reflect.TypeOf(struct{ City string `json:"city"` }{})),
    }},
})
```

### Resilience (Retry + Fallback)

```go
import (
    "github.com/odysseythink/pantheon/extensions/retry"
    "github.com/odysseythink/pantheon/extensions/fallback"
)

resilient := &fallback.Model{
    Candidates: []core.LanguageModel{
        &retry.Model{Inner: openaiModel, MaxRetries: 3},
        anthropicModel,
    },
}
```

## Providers

### Core (main module)

| Provider | Package | Notes |
|----------|---------|-------|
| OpenAI | `providers/openai` | Chat Completions, Responses API |
| Anthropic | `providers/anthropic` | Messages API, reasoning, computer use |
| Google | `providers/google` | Gemini / Vertex AI |
| Azure | `providers/azure` | Azure OpenAI |
| AWS Bedrock | `providers/bedrock` | AWS Bedrock |
| OpenRouter | `providers/openrouter` | OpenAI-compatible routing |
| Ollama | `providers/ollama` | Local models |
| OpenAI-Compatible | `providers/openaicompat` | Generic base for any OpenAI-like API |

### Extra (`providers-extra` module)

| Provider | Package |
|----------|---------|
| DeepSeek | `providers-extra/deepseek` |
| Qwen | `providers-extra/qwen` |

## Architecture

Pantheon is organized into four layers with strict downward dependencies:

```
┌─────────────────────────────────────┐
│  agent/                             │  ← Tool-loop agent engine
├─────────────────────────────────────┤
│  extensions/                        │  ← Retry, fallback, embed, errors
├─────────────────────────────────────┤
│  providers/, providers-extra/       │  ← Provider implementations
├─────────────────────────────────────┤
│  core/                              │  ← Interfaces & types, zero deps
└─────────────────────────────────────┘
```

- **`core/`** — `Provider`, `LanguageModel`, `Message`, `ContentParter`, `ToolDefinition`, `Schema`, streaming primitives. Zero external AI SDK dependencies.
- **`providers/`** — Each sub-package implements `core.Provider` and `core.LanguageModel` for a specific backend.
- **`extensions/`** — Pure composition wrappers over `core.LanguageModel`. Add retry, fallback, embedding, or error classification without changing the interface.
- **`agent/`** — Orchestrates a `LanguageModel` with a tool registry. Handles the full loop: model generates → tools execute → results feed back → repeat until completion.

## Design Principles

1. **One interface, many providers** — `core.LanguageModel` is the only thing your business code needs to know about.
2. **Composition over inheritance** — Extensions are wrapper structs, not interface modifications.
3. **Streaming is not an afterthought** — Every provider supports streaming through the same `iter.Seq2` API.
4. **No global registries** — Providers are instantiated explicitly. No init-side effects.
5. **Go idiomatic** — Uses Go 1.23+ features (`iter.Seq2`, `slog`) where they improve ergonomics.

## Roadmap

| Phase | Content | Version |
|-------|---------|---------|
| Phase 1 | Core + core providers | v0.1.0 |
| Phase 2 | Extensions (retry, fallback, embed) | v0.2.0 |
| Phase 3 | Agent engine + context compression | v0.3.0 |
| Phase 4 | Extra providers migration | v0.4.0 |
| Phase 5 | API stabilization | v1.0.0 |

## License

MIT
