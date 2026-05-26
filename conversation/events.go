package conversation

// StartHandler is called when a conversation starts.
type StartHandler func(chat Chat, conv *Conversation)

// MessageHandler is called when a new message is added to history.
type MessageHandler func(chat Chat, conv *Conversation)

// ErrorHandler is called when an error occurs during a chat turn.
type ErrorHandler func(err error, route Route, conv *Conversation)

// TerminateHandler is called when the conversation terminates.
type TerminateHandler func(node string, conv *Conversation)

// InterruptHandler is called when the conversation is interrupted.
type InterruptHandler func(route Route, conv *Conversation)
