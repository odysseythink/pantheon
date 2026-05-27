## Pantheon v0.0.9

### ✨ New Features

#### Multi-Agent Conversation Framework (`conversation/`)
- **New top-level package** `conversation/` — a Go port of the aibitat multi-agent conversation framework.
- **Core orchestrator** `Conversation` with iterative `runLoop` (non-recursive), thread-safe via `sync.RWMutex`.
- **Participants & Channels**: register agents for direct messaging or group chat with model-based speaker selection.
- **Event system**: `OnStart`, `OnMessage`, `OnError`, `OnTerminate`, `OnInterrupt` with thread-safe emission.
- **Flow control**: support for `TERMINATE`, `INTERRUPT`, `Continue`, and `Retry` with per-channel and global `MaxRounds` limits.
- **Plugins**:
  - **CLI plugin** — terminal I/O with optional stream simulation, interrupt feedback, and retryable-error backoff.
  - **FileHistory plugin** — persists chat history to timestamped JSON files.
  - **WebBrowsing plugin** — search (Serper) and scrape (Browserless) with HTML-to-Markdown conversion and auto-summarization.

#### Reranker Support (`extensions/rerank/`)
- New `rerank.Provider` and `rerank.RerankModel` interfaces.
- `openaicompat.Client` gains `CreateRerank` supporting **OpenAI-compatible**, **Jina**, and **Cohere v2** formats.
- `openai` provider implements `rerank.Provider` via delegation to `openaicompat`.

#### Native Embedding Provider (`providers/native/`)
- Local embedding using the **Cybertron** library.
- Supports models like `sentence-transformers/all-MiniLM-L6-v2`, `nomic-ai/nomic-embed-text-v1`, etc.

#### Native Reranker Provider (`providers/native/`)
- Local cross-encoder reranker using **Cybertron** BERT.
- Full `rerank.RerankModel` implementation with cosine-similarity scoring.

#### 20+ New LLM Providers (Batch 3)
Added providers for: **Mistral**, **Cohere**, **LiteLLM**, **LMStudio**, **LocalAI**, **GenericOpenAI**, **Lemonade**, **Voyage**, **Azure** (embedding), **Ollama** (embedding), **OpenRouter** (embedding), **Google** (Gemini embedding), plus 20 additional cloud/regional/local providers.

### 🔧 Improvements

- `openaicompat.Client` now supports `reasoning_content` fallback for models that place output in the reasoning field (e.g., DeepSeek-R1, Qwen3-thinking).
- `conversation.Conversation` adds `WithHistory` option for pre-loading historical chats.
- Channel-level `MaxRounds` overrides global limit for fine-grained group-chat control.

### ✅ Testing

- `conversation/` core package: **79.0%** coverage (26 tests, race-detector clean).
- `conversation/plugins/`: **92.4%** coverage with real-model integration tests via local `kb-big`.
- `extensions/embed/` and `extensions/rerank/`: integration tests against local `Qwen/Qwen3-Embedding-0.6B` and `Qwen/Qwen3-Reranker-0.6B`.
- All API keys consumed from `OPENAI_API_KEY` env var; integration tests auto-skip when unset.

### 🐛 Fixes

- Fixed `openaicompat.ToCoreResponse` to handle models returning `null` content with `reasoning_content`.
- Fixed CLI plugin deadlock in `Use()` by moving `p.Setup()` outside the lock.
- Fixed FileHistory plugin race condition on concurrent file writes.

### 📦 Dependencies

- `github.com/JohannesKaufmann/html-to-markdown` (WebBrowsing plugin)

---

**Full Changelog**: https://github.com/odysseythink/pantheon/compare/v0.0.8...v0.0.9
