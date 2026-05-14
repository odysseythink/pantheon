package benchmark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderReport(t *testing.T) {
	dir := t.TempDir()
	rub, err := os.Create(filepath.Join(dir, "rubric.jsonl"))
	require.NoError(t, err)
	enc := json.NewEncoder(rub)
	enc.Encode(RubricScore{PresetName: "a", InputID: "gen_1", Correctness: 8, MemoryRelevance: 6, SkillApplied: 5, Overall: 7})
	enc.Encode(RubricScore{PresetName: "b", InputID: "gen_1", Correctness: 9, MemoryRelevance: 8, SkillApplied: 7, Overall: 8})
	rub.Close()

	pair, err := os.Create(filepath.Join(dir, "pairwise.jsonl"))
	require.NoError(t, err)
	enc2 := json.NewEncoder(pair)
	enc2.Encode(PairwiseVerdict{InputID: "gen_1", PresetA: "a", PresetB: "b", Consensus: "b"})
	pair.Close()

	out := filepath.Join(dir, "report.md")
	require.NoError(t, Render(context.Background(), RenderConfig{RunDir: dir, OutPath: out}))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, "# Benchmark Report")
	assert.Contains(t, content, "b wins: 1 / 1")
	assert.Contains(t, content, "| a")
	assert.Contains(t, content, "| b")

	jsonOut := filepath.Join(dir, "report.json")
	_, err = os.Stat(jsonOut)
	assert.NoError(t, err)
}
