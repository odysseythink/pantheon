package conversation

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/stretchr/testify/require"
)

// selectorModel always returns a fixed response.
type selectorModel struct {
	fixed string
}

func (m *selectorModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return &core.Response{
		Message: core.Message{Content: core.NewTextContent(m.fixed)},
	}, nil
}

func (m *selectorModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (m *selectorModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *selectorModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *selectorModel) Provider() string {
	return "selector"
}

func (m *selectorModel) Model() string {
	return "selector-model"
}

func TestConversation_SelectNext_ModelSelectsCorrectly(t *testing.T) {
	selector := &selectorModel{fixed: "bob"}
	c := New()
	c.RegisterParticipant(&Participant{Name: "alice"})
	c.RegisterParticipant(&Participant{Name: "bob"})
	c.RegisterChannel(&Channel{
		Name:    "team",
		Members: []string{"alice", "bob"},
		Model:   selector,
	})

	next, err := c.selectNext(context.Background(), "team")
	require.NoError(t, err)
	require.Equal(t, "bob", next)
}

func TestConversation_SelectNext_FallbackToRandom(t *testing.T) {
	c := New()
	c.RegisterParticipant(&Participant{Name: "a"})
	c.RegisterParticipant(&Participant{Name: "b"})
	c.RegisterChannel(&Channel{
		Name:    "team",
		Members: []string{"a", "b"},
	})

	next, err := c.selectNext(context.Background(), "team")
	require.NoError(t, err)
	require.Contains(t, []string{"a", "b"}, next)
}

func TestConversation_SelectNext_ExcludesLastSpeaker(t *testing.T) {
	c := New()
	c.RegisterParticipant(&Participant{Name: "a"})
	c.RegisterParticipant(&Participant{Name: "b"})
	c.RegisterChannel(&Channel{
		Name:    "team",
		Members: []string{"a", "b"},
	})

	// Inject history where "a" was the last speaker
	c.mu.Lock()
	c.history = append(c.history, Chat{From: "a", To: "team", Content: "hi", State: ChatStateSuccess})
	c.mu.Unlock()

	next, err := c.selectNext(context.Background(), "team")
	require.NoError(t, err)
	require.Equal(t, "b", next)
}

func TestConversation_SelectNext_EmptyGroup(t *testing.T) {
	c := New()
	c.RegisterChannel(&Channel{Name: "empty", Members: []string{}})

	_, err := c.selectNext(context.Background(), "empty")
	require.ErrorIs(t, err, ErrEmptyGroup)
}
