package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odysseythink/pantheon/core"
)

// GenerateConfig parameterizes dataset generation.
type GenerateConfig struct {
	Count   int
	Seed    int64
	OutPath string
	Model   core.LanguageModel
	ModelID string
}

const generateSystemPrompt = `Generate a JSON array of diverse user messages testing an AI agent's ability to solve coding problems, explain concepts, use tools, and follow multi-step instructions. Return ONLY the JSON array, no fences or commentary.`

// Generate asks the aux model for a JSON array of input items and
// writes them to a JSONL file with a meta first line.
func Generate(ctx context.Context, cfg GenerateConfig) error {
	prompt := fmt.Sprintf(`Produce %d items. Each item: {"id": "gen_NNN", "category": "...", "message": "..."}. Use deterministic ordering based on seed=%d.`, cfg.Count, cfg.Seed)

	resp, err := cfg.Model.Generate(ctx, &core.Request{
		SystemPrompt: generateSystemPrompt,
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: prompt}}},
		},
	})
	if err != nil {
		return fmt.Errorf("benchmark: generate llm: %w", err)
	}

	var raw string
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			raw += p.Text
		}
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var items []InputItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return fmt.Errorf("benchmark: generate parse: %w (raw=%q)", err, raw)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.OutPath), 0o755); err != nil {
		return fmt.Errorf("benchmark: mkdir: %w", err)
	}
	f, err := os.Create(cfg.OutPath)
	if err != nil {
		return fmt.Errorf("benchmark: create: %w", err)
	}
	defer f.Close()

	meta := map[string]DatasetMeta{"__meta": {
		Seed:        cfg.Seed,
		Model:       cfg.ModelID,
		GeneratedAt: time.Now().UTC(),
		Count:       len(items),
	}}
	enc := json.NewEncoder(f)
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("benchmark: write meta: %w", err)
	}
	for _, it := range items {
		if strings.TrimSpace(it.Message) == "" {
			continue
		}
		if err := enc.Encode(it); err != nil {
			return fmt.Errorf("benchmark: write item: %w", err)
		}
	}
	return nil
}

// LoadDataset parses a synthetic-benchmark dataset JSONL into Items.
// First-line meta records (objects with a "__meta" key) are skipped.
// Returns []Item so it can satisfy the LoaderFn signature directly.
func LoadDataset(path string) ([]Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("benchmark: open dataset: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	var items []Item
	for s.Scan() {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(s.Bytes(), &probe); err != nil {
			continue
		}
		if _, isMeta := probe["__meta"]; isMeta {
			continue
		}
		var it InputItem
		if err := json.Unmarshal(s.Bytes(), &it); err != nil {
			continue
		}
		if it.ID == "" {
			continue
		}
		items = append(items, it)
	}
	return items, s.Err()
}
