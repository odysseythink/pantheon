package conversation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConversation_Events(t *testing.T) {
	c := New()

	var started bool
	c.OnStart(func(chat Chat, conv *Conversation) {
		started = true
		require.Equal(t, "hello", chat.Content)
	})

	var messages []string
	c.OnMessage(func(chat Chat, conv *Conversation) {
		messages = append(messages, chat.Content)
	})

	var terminated bool
	c.OnTerminate(func(node string, conv *Conversation) {
		terminated = true
	})

	c.emitStart(Chat{Content: "hello"})
	require.True(t, started)

	c.emitMessage(Chat{Content: "msg1"})
	c.emitMessage(Chat{Content: "msg2"})
	require.Equal(t, []string{"msg1", "msg2"}, messages)

	c.emitTerminate("bot")
	require.True(t, terminated)
}

func TestConversation_EventConcurrency(t *testing.T) {
	c := New()
	var count int
	c.OnMessage(func(chat Chat, conv *Conversation) {
		count++
	})

	// Register another handler while emitting
	go c.OnMessage(func(chat Chat, conv *Conversation) {
		count++
	})

	c.emitMessage(Chat{Content: "test"})
	// Should not panic; exact count is non-deterministic due to goroutine timing
}
