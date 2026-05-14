package benchmark

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunWritesRecords(t *testing.T) {
	dir := t.TempDir()
	ds := filepath.Join(dir, "ds.jsonl")

	// Write a minimal dataset.
	f, err := os.Create(ds)
	require.NoError(t, err)
	enc := json.NewEncoder(f)
	require.NoError(t, enc.Encode(map[string]DatasetMeta{"__meta": {Count: 1}}))
	require.NoError(t, enc.Encode(InputItem{ID: "gen_1", Category: "coding", Message: "hi"}))
	f.Close()

	cfg := RunConfig{
		DatasetPath: ds,
		OutDir:      dir,
		Presets: map[string]PresetRunner{
			"a": func(_ context.Context, item Item) (*RunRecord, error) {
				return &RunRecord{Reply: "reply-a-" + item.GetMessage()}, nil
			},
		},
	}
	require.NoError(t, Run(context.Background(), cfg))

	records := filepath.Join(dir, "a", "records.jsonl")
	data, err := os.ReadFile(records)
	require.NoError(t, err)
	s := bufio.NewScanner(bytes.NewReader(data))
	var record RunRecord
	s.Scan()
	require.NoError(t, json.Unmarshal(s.Bytes(), &record))
	assert.Equal(t, "gen_1", record.InputID)
	assert.Contains(t, record.Reply, "reply-a-")
	assert.Equal(t, "a", record.PresetName)
}

func TestRunSkipsDoneInputs(t *testing.T) {
	dir := t.TempDir()
	ds := filepath.Join(dir, "ds.jsonl")
	f, _ := os.Create(ds)
	enc := json.NewEncoder(f)
	enc.Encode(map[string]DatasetMeta{"__meta": {Count: 2}})
	enc.Encode(InputItem{ID: "gen_1", Message: "one"})
	enc.Encode(InputItem{ID: "gen_2", Message: "two"})
	f.Close()

	// Pre-populate "a"/records.jsonl with gen_1 already done.
	preset := filepath.Join(dir, "a")
	require.NoError(t, os.MkdirAll(preset, 0o755))
	rf, _ := os.Create(filepath.Join(preset, "records.jsonl"))
	enc2 := json.NewEncoder(rf)
	enc2.Encode(RunRecord{PresetName: "a", InputID: "gen_1", Reply: "prior"})
	rf.Close()

	calls := 0
	cfg := RunConfig{
		DatasetPath: ds,
		OutDir:      dir,
		Presets: map[string]PresetRunner{
			"a": func(_ context.Context, item Item) (*RunRecord, error) {
				calls++
				return &RunRecord{Reply: "new-" + item.GetMessage()}, nil
			},
		},
	}
	require.NoError(t, Run(context.Background(), cfg))
	assert.Equal(t, 1, calls, "only gen_2 should be freshly run")
}

func TestRun_CustomLoaderFn(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(out, 0o755))

	// Custom loader returns a fixed slice — does NOT read DatasetPath.
	loader := func(_ string) ([]Item, error) {
		return []Item{
			InputItem{ID: "x", Message: "hello"},
			InputItem{ID: "y", Message: "world"},
		}, nil
	}

	calls := 0
	runner := func(ctx context.Context, item Item) (*RunRecord, error) {
		calls++
		return &RunRecord{Reply: "ok-" + item.GetID()}, nil
	}

	cfg := RunConfig{
		DatasetPath: "/does/not/exist", // ignored when LoaderFn is set
		OutDir:      out,
		Presets:     map[string]PresetRunner{"a": runner},
		LoaderFn:    loader,
	}
	require.NoError(t, Run(context.Background(), cfg))
	require.Equal(t, 2, calls)
}
