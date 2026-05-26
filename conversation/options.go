package conversation

// Option configures a Conversation.
type Option func(*Conversation)

// WithMaxRounds sets the global maximum number of chat rounds.
func WithMaxRounds(n int) Option {
	return func(c *Conversation) {
		c.maxRounds = n
	}
}

// WithHistory sets the initial chat history.
func WithHistory(history []Chat) Option {
	return func(c *Conversation) {
		c.history = append([]Chat(nil), history...)
	}
}
