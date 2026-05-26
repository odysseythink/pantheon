package conversation

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/odysseythink/pantheon/core"
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

func (c *Conversation) newMessage(route Route, content string) {
	chat := Chat{
		From:    route.From,
		To:      route.To,
		Content: content,
		State:   ChatStateSuccess,
	}
	c.mu.Lock()
	c.history = append(c.history, chat)
	c.mu.Unlock()
	c.emitMessage(chat)
}

func (c *Conversation) newError(route Route, err error) {
	chat := Chat{
		From:    route.From,
		To:      route.To,
		Content: err.Error(),
		State:   ChatStateError,
	}
	c.mu.Lock()
	c.history = append(c.history, chat)
	c.mu.Unlock()
	c.emitError(err, route)
}

func (c *Conversation) terminate(node string) {
	c.emitTerminate(node)
}

func (c *Conversation) interrupt(route Route) {
	chat := Chat{
		From:    route.From,
		To:      route.To,
		State:   ChatStateInterrupt,
	}
	c.mu.Lock()
	c.history = append(c.history, chat)
	c.mu.Unlock()
	c.emitInterrupt(route)
}

func (c *Conversation) hasReachedMaxRounds(from, to string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, chat := range c.history {
		if chat.State != ChatStateSuccess {
			continue
		}
		if (chat.From == from && chat.To == to) || (chat.From == to && chat.To == from) {
			count++
		}
	}
	return count >= c.maxRounds
}

func (c *Conversation) getHistory(from, to string) []Chat {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Chat
	for _, chat := range c.history {
		if chat.State != ChatStateSuccess {
			continue
		}
		if from == "" && chat.To == to {
			out = append(out, chat)
			continue
		}
		if to == "" && chat.From == from {
			out = append(out, chat)
			continue
		}
		if (chat.From == from && chat.To == to) || (chat.From == to && chat.To == from) {
			out = append(out, chat)
		}
	}
	return out
}

// Start begins a new conversation.
func (c *Conversation) Start(ctx context.Context, from, to, content string) error {
	msg := Chat{
		From:    from,
		To:      to,
		Content: content,
		State:   ChatStateSuccess,
	}
	c.mu.Lock()
	c.history = append(c.history, msg)
	c.mu.Unlock()
	c.emitStart(msg)

	return c.runLoop(ctx, Route{From: to, To: from})
}

func (c *Conversation) runLoop(ctx context.Context, start Route) error {
	route := start
	for {
		if c.hasReachedMaxRounds(route.From, route.To) {
			c.terminate(route.To)
			return nil
		}

		if c.isChannel(route.From) {
			next, err := c.selectNext(ctx, route.From)
			if err != nil {
				c.newError(route, err)
				return err
			}
			if next == "" {
				c.terminate(route.From)
				return nil
			}
			route = Route{From: next, To: route.From}
			if c.shouldInterrupt(next) {
				c.interrupt(route)
				return nil
			}
			continue
		}

		reply, err := c.reply(ctx, route)
		if err != nil {
			c.newError(route, err)
			return err
		}

		if reply == "TERMINATE" || c.hasReachedMaxRounds(route.From, route.To) {
			c.terminate(route.To)
			return nil
		}

		if reply == "INTERRUPT" || c.shouldInterrupt(route.To) {
			c.interrupt(Route{From: route.To, To: route.From})
			return nil
		}

		route = Route{From: route.To, To: route.From}
	}
}

func (c *Conversation) reply(ctx context.Context, route Route) (string, error) {
	fromP, err := c.getParticipant(route.From)
	if err != nil {
		return "", err
	}

	var messages []string
	if c.isChannel(route.To) {
		history := c.getHistory("", route.To)
		messages = append(messages, fmt.Sprintf("You are in a group chat. Read the following conversation and reply.\nDo not add introduction or conclusion.\n\nCHAT HISTORY"))
		for _, h := range history {
			messages = append(messages, fmt.Sprintf("@%s: %s", h.From, h.Content))
		}
		messages = append(messages, fmt.Sprintf("@%s:", route.From))
	} else {
		history := c.getHistory(route.From, route.To)
		for _, h := range history {
			role := "user"
			if h.From == route.From {
				role = "assistant"
			}
			messages = append(messages, fmt.Sprintf("%s: %s", role, h.Content))
		}
	}

	var content string
	if fromP.Agent != nil {
		req := &core.Request{
			SystemPrompt: fromP.Role,
			Messages:     []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_USER, strings.Join(messages, "\n"))},
		}
		result, err := fromP.Agent.Run(ctx, req)
		if err != nil {
			return "", err
		}
		if len(result.Messages) > 0 {
			content = result.Messages[len(result.Messages)-1].Text()
		}
	} else if fromP.Model != nil {
		req := &core.Request{
			SystemPrompt: fromP.Role,
			Messages:     []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_USER, strings.Join(messages, "\n"))},
		}
		resp, err := fromP.Model.Generate(ctx, req)
		if err != nil {
			return "", err
		}
		content = resp.Message.Text()
	} else {
		return "", fmt.Errorf("participant %q has no model or agent", route.From)
	}

	c.newMessage(route, content)
	return content, nil
}

// selectNext is a stub for Task 6; it will be replaced by the real implementation.
func (c *Conversation) selectNext(ctx context.Context, channelName string) (string, error) {
	return "", nil
}
