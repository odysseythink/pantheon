package conversation

// Plugin extends a Conversation with additional behavior.
type Plugin interface {
	Name() string
	Setup(conv *Conversation) error
}
