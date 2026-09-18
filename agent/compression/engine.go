package compression

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// ContextEngine abstracts context compression strategies.
type ContextEngine interface {
	Name() string
	UpdateFromResponse(usage core.Usage) error
	ShouldCompress(promptTokens int) bool
	CompressMessages(ctx context.Context, messages []core.Message, focusTopic string) ([]core.Message, error)
	UpdateModel(model string, contextLength int) error
	GetToolSchemas() []core.ToolDefinition
	HandleToolCall(ctx context.Context, name string, args map[string]any) (string, error)
}
