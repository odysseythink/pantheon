package judge

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/odysseythink/pantheon/core"
)

type judgeStubProvider struct {
	reply string
	err   error
}

func (s *judgeStubProvider) Provider() string { return "stub" }
func (s *judgeStubProvider) Model() string    { return "stub-model" }
func (s *judgeStubProvider) Generate(_ context.Context, _ *core.Request) (*core.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: []core.ContentParter{core.TextPart{Text: s.reply}},
		},
	}, nil
}
func (s *judgeStubProvider) Stream(_ context.Context, _ *core.Request) (core.StreamResponse, error) {
	panic("stub stream")
}
func (s *judgeStubProvider) GenerateObject(_ context.Context, _ *core.ObjectRequest) (*core.ObjectResponse, error) {
	panic("stub object")
}

func TestLLMJudgeParsesVerdict(t *testing.T) {
	p := &judgeStubProvider{reply: `{"outcome":"struggle","memories_used":["mc_1"],"skills_to_extract":[{"name":"n","description":"d","body":"b"}],"reasoning":"retried"}`}
	j := NewLLM(p)
	v, err := j.Run(context.Background(), Input{
		History:          []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hi"}}}},
		InjectedMemories: []InjectedMemory{{ID: "mc_1", Content: "fact"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "struggle", v.Outcome)
	assert.Equal(t, []string{"mc_1"}, v.MemoriesUsed)
	require.Len(t, v.SkillsToExtract, 1)
	assert.Equal(t, "n", v.SkillsToExtract[0].Name)
}

func TestLLMJudgeHandlesFences(t *testing.T) {
	p := &judgeStubProvider{reply: "```json\n{\"outcome\":\"success\"}\n```"}
	v, err := NewLLM(p).Run(context.Background(), Input{})
	require.NoError(t, err)
	assert.Equal(t, "success", v.Outcome)
}

func TestLLMJudgeMalformedReturnsUnknown(t *testing.T) {
	p := &judgeStubProvider{reply: "not json"}
	v, err := NewLLM(p).Run(context.Background(), Input{})
	require.NoError(t, err)
	assert.Equal(t, "unknown", v.Outcome)
}

func TestLLMJudgeProviderErrorReturnsUnknown(t *testing.T) {
	p := &judgeStubProvider{err: errors.New("aux down")}
	v, err := NewLLM(p).Run(context.Background(), Input{})
	require.NoError(t, err)
	assert.Equal(t, "unknown", v.Outcome)
}

func TestLLMJudgeNilProviderReturnsUnknown(t *testing.T) {
	v, err := NewLLM(nil).Run(context.Background(), Input{})
	require.NoError(t, err)
	assert.Equal(t, "unknown", v.Outcome)
}
