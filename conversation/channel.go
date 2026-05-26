package conversation

import "github.com/odysseythink/pantheon/core"

// Channel is a group of participants that can chat together.
type Channel struct {
	Name      string
	Members   []string
	Role      string
	MaxRounds int
	Model     core.LanguageModel
}
