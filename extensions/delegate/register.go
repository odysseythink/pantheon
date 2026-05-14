package delegate

import (
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

const delegateSchema = `{
  "type": "object",
  "properties": {
    "task":          { "type": "string", "description": "A specific, self-contained task for the subagent to complete" },
    "context":       { "type": "string", "description": "Optional background context" },
    "max_turns":     { "type": "number", "description": "Max turns the subagent may take (default 20, max 50)" }
  },
  "required": ["task"]
}`

// RegisterDelegate registers the delegate tool bound to a SubagentRunner.
// If runner is nil, the tool is still registered but returns an error
// at dispatch time.
func RegisterDelegate(reg *tool.Registry, runner SubagentRunner) {
	reg.Register(&tool.Entry{
		Name:        "delegate",
		Toolset:     "delegate",
		Description: "Delegate a self-contained task to a subagent. The subagent has its own budget and history.",
		Emoji:       "👥",
		Handler:     newDelegateHandler(runner),
		Schema: core.ToolDefinition{
			Name:        "delegate",
			Description: "Run a fresh subagent on a specific, self-contained task.",
			Parameters:  core.MustSchemaFromJSON([]byte(delegateSchema)),
		},
	})
}
