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

type judgeStub struct{ reply string }

func (j *judgeStub) Provider() string { return "judge" }
func (j *judgeStub) Model() string    { return "" }

func (j *judgeStub) Generate(_ context.Context, _ *core.Request) (*core.Response, error) {
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: []core.ContentParter{core.TextPart{Text: j.reply}},
		},
	}, nil
}

func (j *judgeStub) Stream(_ context.Context, _ *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (j *judgeStub) GenerateObject(_ context.Context, _ *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func TestJudgeAllProducesRubricAndPairwise(t *testing.T) {
	dir := t.TempDir()
	for _, preset := range []string{"a", "b"} {
		p := filepath.Join(dir, preset)
		require.NoError(t, os.MkdirAll(p, 0o755))
		f, err := os.Create(filepath.Join(p, "records.jsonl"))
		require.NoError(t, err)
		enc := json.NewEncoder(f)
		enc.Encode(RunRecord{PresetName: preset, InputID: "gen_1", Reply: "reply-" + preset})
		f.Close()
	}

	rubricStub := &judgeStub{reply: `{"correctness":8,"memory_relevance":7,"skill_applied":5,"overall":7,"reason":"ok"}`}
	pairwiseStub := &judgeStub{reply: `{"winner":"a","reason":"stub"}`}
	cfg := JudgeConfig{
		RunDir:           dir,
		RubricProvider:   rubricStub,
		PairwiseProvider: pairwiseStub,
	}
	require.NoError(t, JudgeAll(context.Background(), cfg))

	rubricPath := filepath.Join(dir, "rubric.jsonl")
	pairPath := filepath.Join(dir, "pairwise.jsonl")

	rubricLines := readLines(t, rubricPath)
	assert.Len(t, rubricLines, 2)
	var rub RubricScore
	require.NoError(t, json.Unmarshal([]byte(rubricLines[0]), &rub))
	assert.Equal(t, 8, rub.Correctness)

	pairLines := readLines(t, pairPath)
	assert.Len(t, pairLines, 1)
	var pv PairwiseVerdict
	require.NoError(t, json.Unmarshal([]byte(pairLines[0]), &pv))
	// Both swaps return winner="a". That means the first position always wins.
	// In ab (a, b): "a" wins = preset a wins.
	// In ba (b, a): "a" wins = preset b wins.
	// Disagreement → consensus tie.
	assert.Equal(t, "tie", pv.Consensus)
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		out = append(out, s.Text())
	}
	return out
}
