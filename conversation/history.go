package conversation

// Chat is a single message in the conversation history.
type Chat struct {
	From    string
	To      string
	Content string
	State   ChatState
}

// ChatState represents the outcome of a chat turn.
type ChatState string

const (
	ChatStateSuccess   ChatState = "success"
	ChatStateInterrupt ChatState = "interrupt"
	ChatStateError     ChatState = "error"
)

// Route identifies the sender and receiver of a message.
type Route struct {
	From string
	To   string
}
