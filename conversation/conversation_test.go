package conversation

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/stretchr/testify/require"
)

func TestConversation_RegisterAndChats(t *testing.T) {
	c := New()

	c.RegisterParticipant(&Participant{Name: "alice"})
	c.RegisterParticipant(&Participant{Name: "bob"})
	c.RegisterChannel(&Channel{Name: "general", Members: []string{"alice", "bob"}})

	require.Len(t, c.participants, 2)
	require.Len(t, c.channels, 1)
	require.Empty(t, c.Chats())
}

func TestConversation_GetParticipant_NotFound(t *testing.T) {
	c := New()
	_, err := c.getParticipant("nobody")
	require.ErrorIs(t, err, ErrParticipantNotFound)
}

func TestConversation_GetChannel_NotFound(t *testing.T) {
	c := New()
	_, err := c.getChannel("nowhere")
	require.ErrorIs(t, err, ErrChannelNotFound)
}

// mockModel implements core.LanguageModel for testing.
type mockModel struct {
	responses []string
	index     int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.index >= len(m.responses) {
		return &core.Response{Message: core.Message{Content: core.NewTextContent("TERMINATE")}}, nil
	}
	resp := m.responses[m.index]
	m.index++
	return &core.Response{
		Message: core.Message{Content: core.NewTextContent(resp)},
		Usage:   core.Usage{},
	}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (m *mockModel) Provider() string {
	return "mock"
}

func (m *mockModel) Model() string {
	return "mock-model"
}

func TestConversation_DirectMessage_Terminate(t *testing.T) {
	model := &mockModel{responses: []string{"Hello", "TERMINATE"}}

	c := New(WithMaxRounds(10))
	c.RegisterParticipant(&Participant{Name: "user", Model: model})
	c.RegisterParticipant(&Participant{Name: "bot", Model: model})

	err := c.Start(context.Background(), "user", "bot", "Hi")
	require.NoError(t, err)

	chats := c.Chats()
	require.Len(t, chats, 2)
	require.Equal(t, "Hi", chats[0].Content)
	require.Equal(t, "Hello", chats[1].Content)
}

func TestConversation_DirectMessage_MaxRounds(t *testing.T) {
	model := &mockModel{responses: []string{"reply1", "reply2", "reply3"}}

	c := New(WithMaxRounds(2))
	c.RegisterParticipant(&Participant{Name: "a", Model: model})
	c.RegisterParticipant(&Participant{Name: "b", Model: model})

	err := c.Start(context.Background(), "a", "b", "start")
	require.NoError(t, err)

	// 2 rounds = a->b, b->a (terminated before a replies again)
	chats := c.Chats()
	require.Len(t, chats, 2)
}

func TestConversation_Continue(t *testing.T) {
	model := &mockModel{responses: []string{"Hello", "INTERRUPT"}}

	c := New(WithMaxRounds(10))
	c.RegisterParticipant(&Participant{Name: "user", Model: model, Interrupt: InterruptAlways})
	c.RegisterParticipant(&Participant{Name: "bot", Model: model})

	err := c.Start(context.Background(), "user", "bot", "Hi")
	require.NoError(t, err)

	require.Equal(t, ChatStateInterrupt, c.Chats()[len(c.Chats())-1].State)

	err = c.Continue(context.Background(), "Please continue")
	require.NoError(t, err)

	// Find the feedback message in history
	var found bool
	for _, chat := range c.Chats() {
		if chat.Content == "Please continue" {
			found = true
			break
		}
	}
	require.True(t, found, "feedback should be in history")
}

func TestConversation_Continue_EmptyFeedback(t *testing.T) {
	model := &mockModel{responses: []string{"Hello", "reply"}}

	c := New(WithMaxRounds(10))
	c.RegisterParticipant(&Participant{Name: "user", Model: model, Interrupt: InterruptAlways})
	c.RegisterParticipant(&Participant{Name: "bot", Model: model})

	err := c.Start(context.Background(), "user", "bot", "Hi")
	require.NoError(t, err)

	err = c.Continue(context.Background(), "")
	require.NoError(t, err)

	// Hi, Hello, interrupt, reply (TERMINATE not stored)
	chats := c.Chats()
	require.Equal(t, "reply", chats[len(chats)-1].Content)
}

func TestConversation_Retry(t *testing.T) {
	failModel := &mockModel{responses: []string{"reply"}}
	c := New()
	c.RegisterParticipant(&Participant{Name: "a", Model: failModel})
	c.RegisterParticipant(&Participant{Name: "b", Model: failModel})

	// Manually inject an error chat
	c.mu.Lock()
	c.history = append(c.history, Chat{From: "a", To: "b", State: ChatStateError})
	c.mu.Unlock()

	err := c.Retry(context.Background())
	require.NoError(t, err)
	require.Equal(t, ChatStateSuccess, c.Chats()[len(c.Chats())-1].State)
}

func TestConversation_Start_ValidatesParticipants(t *testing.T) {
	c := New()
	c.RegisterParticipant(&Participant{Name: "alice", Model: &mockModel{}})

	err := c.Start(context.Background(), "alice", "nobody", "Hi")
	require.ErrorIs(t, err, ErrParticipantNotFound)
	require.Empty(t, c.Chats())
}

func TestConversation_ChannelFlow(t *testing.T) {
	selector := &mockModel{responses: []string{"bob", "TERMINATE"}}
	botModel := &mockModel{responses: []string{"Hello", "TERMINATE"}}

	c := New(WithMaxRounds(10))
	c.RegisterParticipant(&Participant{Name: "alice", Model: botModel})
	c.RegisterParticipant(&Participant{Name: "bob", Model: botModel})
	c.RegisterChannel(&Channel{
		Name:    "general",
		Members: []string{"alice", "bob"},
		Model:   selector,
	})

	err := c.Start(context.Background(), "alice", "general", "Hi everyone")
	require.NoError(t, err)

	chats := c.Chats()
	require.Len(t, chats, 2) // alice->general, bob->general (Hello)
	require.Equal(t, "Hi everyone", chats[0].Content)
	require.Equal(t, "Hello", chats[1].Content)
}
