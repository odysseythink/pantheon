package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

// InjectActive registers a synthetic tool per active skill into the
// provided tool.Registry. The tool (named "skill_<name>") returns the
// skill body so the model can consult it on demand without bloating
// the system prompt.
//
// This is a deliberately simple Phase 10c implementation — a richer
// version (Plan 10d / Plan 15) would let skills declare their own
// handlers or wire external binaries.
func InjectActive(reg *tool.Registry, skillsReg *Registry) {
	for _, s := range skillsReg.Active() {
		name := "skill_" + sanitize(s.Name)
		body := s.Body
		desc := fmt.Sprintf("Fetch the reference body of the %s skill.", s.Name)
		reg.Register(&tool.Entry{
			Name:        name,
			Toolset:     "skills",
			Description: desc,
			Emoji:       "📘",
			Schema: core.ToolDefinition{
				Name:        name,
				Description: desc,
				Parameters:  &core.Schema{Type: "object", Properties: map[string]*core.Schema{}},
			},
			Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
				return tool.Result(map[string]string{"body": body}), nil
			},
		})
	}
}

// sanitize replaces any non-ident rune with '_'.
func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
