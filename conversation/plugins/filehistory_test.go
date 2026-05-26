package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/conversation"
	"github.com/stretchr/testify/require"
)

func TestFileHistoryPlugin_Name(t *testing.T) {
	p := NewFileHistory(FileHistoryConfig{})
	require.Equal(t, "file-history", p.Name())
}

func TestFileHistoryPlugin_DefaultDir(t *testing.T) {
	p := NewFileHistory(FileHistoryConfig{}).(*fileHistoryPlugin)
	require.Equal(t, "history", p.cfg.Dir)
}

func TestFileHistoryPlugin_RealModel_WritesFile(t *testing.T) {
	dir := t.TempDir()
	plugin := NewFileHistory(FileHistoryConfig{Dir: dir})

	c := conversation.New(conversation.WithMaxRounds(2))
	err := c.Use(plugin)
	require.NoError(t, err)

	model := newRealModel(t)
	c.RegisterParticipant(&conversation.Participant{Name: "user", Model: model})
	c.RegisterParticipant(&conversation.Participant{Name: "assistant", Model: model})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = c.Start(ctx, "user", "assistant", "Say a brief greeting")
	require.NoError(t, err)

	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 1)

	data, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
	require.NoError(t, err)
	require.Contains(t, string(data), "user")
	require.Contains(t, string(data), "assistant")
}
