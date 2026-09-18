package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type genStubProvider struct{ reply string }

func (g *genStubProvider) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (g *genStubProvider) Provider() string { return "stub" }
func (g *genStubProvider) Model() string    { return "" }

func (g *genStubProvider) Generate(_ context.Context, _ *core.Request) (*core.Response, error) {
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: []core.ContentParter{core.TextPart{Text: g.reply}},
		},
	}, nil
}

func (g *genStubProvider) Stream(_ context.Context, _ *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (g *genStubProvider) GenerateObject(_ context.Context, _ *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func TestGenerateWritesMetaAndRows(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "ds.jsonl")

	p := &genStubProvider{reply: `[{"id":"gen_1","category":"coding","message":"foo"},{"id":"gen_2","category":"reasoning","message":"bar"}]`}

	err := Generate(context.Background(), GenerateConfig{
		Count: 3, Seed: 42, OutPath: out, Model: p, ModelID: "claude-stub",
	})
	require.NoError(t, err)

	f, err := os.Open(out)
	require.NoError(t, err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.Len(t, lines, 3) // 1 meta + 2 rows

	var meta struct {
		Meta DatasetMeta `json:"__meta"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &meta))
	assert.Equal(t, int64(42), meta.Meta.Seed)
	assert.Equal(t, 2, meta.Meta.Count)

	var item InputItem
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &item))
	assert.Equal(t, "gen_1", item.ID)
}
