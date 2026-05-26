package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/odysseythink/pantheon/conversation"
)

// FileHistoryConfig for the file history plugin.
type FileHistoryConfig struct {
	Dir string
}

// NewFileHistory creates a file history plugin.
func NewFileHistory(cfg FileHistoryConfig) conversation.Plugin {
	if cfg.Dir == "" {
		cfg.Dir = "history"
	}
	return &fileHistoryPlugin{cfg: cfg}
}

type fileHistoryPlugin struct {
	cfg FileHistoryConfig
}

func (p *fileHistoryPlugin) Name() string { return "file-history" }

func (p *fileHistoryPlugin) Setup(conv *conversation.Conversation) error {
	if err := os.MkdirAll(p.cfg.Dir, 0755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}

	filename := filepath.Join(p.cfg.Dir, fmt.Sprintf("chat-history-%s.json", time.Now().Format("20060102-150405")))
	conv.OnMessage(func(chat conversation.Chat, c *conversation.Conversation) {
		data, err := json.MarshalIndent(c.Chats(), "", "  ")
		if err != nil {
			return
		}
		_ = os.WriteFile(filename, data, 0644)
	})
	return nil
}
