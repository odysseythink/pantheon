package conversation

import (
	"fmt"
	"sync"
)

// Conversation orchestrates multi-agent dialogue.
type Conversation struct {
	mu           sync.RWMutex
	participants map[string]*Participant
	channels     map[string]*Channel
	history      []Chat
	maxRounds    int
	plugins      []Plugin

	onStart     []StartHandler
	onMessage   []MessageHandler
	onError     []ErrorHandler
	onTerminate []TerminateHandler
	onInterrupt []InterruptHandler
}

// New creates a new Conversation.
func New(opts ...Option) *Conversation {
	c := &Conversation{
		participants: make(map[string]*Participant),
		channels:     make(map[string]*Channel),
		maxRounds:    100,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RegisterParticipant registers a participant.
func (c *Conversation) RegisterParticipant(p *Participant) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.participants[p.Name] = p
}

// RegisterChannel registers a channel.
func (c *Conversation) RegisterChannel(ch *Channel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.channels[ch.Name] = ch
}

// Chats returns a copy of the chat history.
func (c *Conversation) Chats() []Chat {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Chat, len(c.history))
	copy(out, c.history)
	return out
}

func (c *Conversation) getParticipant(name string) (*Participant, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.participants[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrParticipantNotFound, name)
	}
	return p, nil
}

func (c *Conversation) getChannel(name string) (*Channel, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ch, ok := c.channels[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrChannelNotFound, name)
	}
	return ch, nil
}

func (c *Conversation) isChannel(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.channels[name]
	return ok
}

func (c *Conversation) shouldInterrupt(name string) bool {
	p, err := c.getParticipant(name)
	if err != nil {
		return false
	}
	return p.Interrupt == InterruptAlways
}
