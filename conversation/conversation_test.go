package conversation

import (
	"testing"

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
