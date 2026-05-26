package conversation

import (
	"github.com/odysseythink/pantheon/agent"
	"github.com/odysseythink/pantheon/core"
)

// Participant is an entity that can take part in a conversation.
type Participant struct {
	Name      string
	Role      string
	Model     core.LanguageModel
	Agent     *agent.Agent
	Interrupt InterruptMode
}

// InterruptMode controls whether a participant interrupts the flow.
type InterruptMode string

const (
	InterruptNever  InterruptMode = "NEVER"
	InterruptAlways InterruptMode = "ALWAYS"
)
