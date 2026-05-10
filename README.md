# ai

Unified AI SDK for Go — shared infrastructure across Odysseythink AI projects.

## Structure

- `core/` — Provider/LanguageModel interfaces, message types, streaming
- `providers/` — LLM provider implementations
- `extensions/` — Retry, fallback, embedding (Phase 2)
- `agent/` — Agent engine with tool loop (Phase 3)
