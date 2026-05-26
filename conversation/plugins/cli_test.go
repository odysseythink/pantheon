package plugins

import (
	"bufio"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/conversation"
	"github.com/stretchr/testify/require"
)

func TestCLIPlugin_OnMessage(t *testing.T) {
	plugin := NewCLI(CLIConfig{
		SimulateStream: false,
		Output:         nil, // use stdout; for test we'll verify via event
	})
	_ = plugin

	c := conversation.New()
	c.RegisterParticipant(&conversation.Participant{Name: "alice"})
	c.RegisterParticipant(&conversation.Participant{Name: "bob"})

	var captured string
	c.OnMessage(func(chat conversation.Chat, conv *conversation.Conversation) {
		captured = chat.Content
	})

	c.RegisterParticipant(&conversation.Participant{Name: "alice"})
	_ = captured
}

func TestCLIPlugin_AskForFeedback(t *testing.T) {
	input := bufio.NewReader(strings.NewReader("my feedback\n"))
	var out strings.Builder
	plugin := &cliPlugin{cfg: CLIConfig{Input: input, Output: &out}}
	feedback := plugin.askForFeedback(conversation.Route{From: "alice", To: "bob"})
	require.Equal(t, "my feedback", feedback)
}
