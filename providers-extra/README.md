# providers-extra

Long-tail AI providers for the `ai` SDK.

## Installation

```bash
go get github.com/odysseythink/pantheon/providers-extra
```

## Providers

- `deepseek` — DeepSeek API (OpenAI-compatible)
- `qwen` — Alibaba Qwen API (OpenAI-compatible)

## Usage

```go
import "github.com/odysseythink/pantheon/providers-extra/deepseek"

p, _ := deepseek.New("api-key")
model, _ := p.LanguageModel(ctx, "deepseek-chat")
```
