package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/judge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvolverExtractUsesVerdict(t *testing.T) {
	dir := t.TempDir()
	ev := NewEvolver(nil, dir)
	verdict := &judge.Verdict{
		SkillsToExtract: []judge.SkillDraft{
			{
				Name:        "Reroute on rate-limit",
				Description: "Backoff strategy",
				Body:        "## Reroute on rate-limit\n**When to use:** when provider 429s.\n\nDo this.",
			},
		},
	}
	require.NoError(t, ev.Extract(context.Background(), nil, verdict))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	body, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	assert.Contains(t, string(body), "Reroute on rate-limit")
}

func TestEvolverExtractNilVerdictFallsBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	ev := NewEvolver(nil, dir)
	require.NoError(t, ev.Extract(context.Background(), nil, nil))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no skill files when verdict nil and llm nil")
}

func TestEvolverExtractNoLLM(t *testing.T) {
	dir := t.TempDir()
	ev := NewEvolver(nil, dir)
	turns := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "how do I reset git?"}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "git reset --hard HEAD"}}},
	}
	require.NoError(t, ev.Extract(context.Background(), turns, nil))
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files written without LLM, got %d", len(entries))
	}
}

func TestEvolverSkillDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	ev := NewEvolver(nil, dir)
	_ = ev.Extract(context.Background(), nil, nil)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("skills dir not created")
	}
}

func TestEvolverOnExtractedCalled(t *testing.T) {
	dir := t.TempDir()
	ev := NewEvolver(nil, dir)
	var extracted []string
	ev.OnExtracted = func(filename, reason string) {
		extracted = append(extracted, filename+":"+reason)
	}
	verdict := &judge.Verdict{
		SkillsToExtract: []judge.SkillDraft{
			{Name: "a", Body: "## A\nbody"},
			{Name: "b", Body: "## B\nbody"},
		},
	}
	require.NoError(t, ev.Extract(context.Background(), nil, verdict))
	if len(extracted) != 2 {
		t.Fatalf("expected 2 OnExtracted calls, got %d", len(extracted))
	}
}

func TestEvolverLegacyPathWithLLM(t *testing.T) {
	dir := t.TempDir()
	mock := &mockProvider{resp: "## Test Skill\n\n**When to use:** test\n\nDo this."}
	ev := NewEvolver(mock, dir)
	turns := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}},
	}
	require.NoError(t, ev.Extract(context.Background(), turns, nil))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

type mockProvider struct {
	name      string
	err       error
	resp      string
	callCount int
}

func (m *mockProvider) Provider() string { return m.name }
func (m *mockProvider) Model() string    { return "test-model" }
func (m *mockProvider) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *mockProvider) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.callCount++
	if m.err != nil {
		return nil, m.err
	}
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: []core.ContentParter{core.TextPart{Text: m.resp}},
		},
	}, nil
}
func (m *mockProvider) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, errors.New("not implemented")
}
func (m *mockProvider) GenerateObject(context.Context, *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}
