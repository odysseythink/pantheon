// Package trajectory writes JSON-Lines event traces of an agent
// conversation to disk. Each Write call appends one event.
package trajectory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/odysseythink/pantheon/core"
)

// Event is one line in the trajectory dump.
type Event struct {
	Time     time.Time   `json:"time"`
	Kind     string      `json:"kind"` // "user", "assistant", "tool_call", "tool_result", "usage"
	Content  string      `json:"content,omitempty"`
	ToolName string      `json:"tool_name,omitempty"`
	Usage    *core.Usage `json:"usage,omitempty"`
}

// Writer appends Event values to a single file in JSON-Lines format.
// Thread-safe.
type Writer struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// New opens or creates <dir>/<sessionID>.jsonl. The directory is
// created with mode 0755 if it does not exist.
func New(dir, sessionID string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trajectory: open %s: %w", path, err)
	}
	return &Writer{path: path, f: f}, nil
}

// Write appends an event to the file. If ev.Time is zero, time.Now().UTC()
// is filled in.
func (w *Writer) Write(ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	buf, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = w.f.Write(buf)
	return err
}

// Close flushes and releases the file handle.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Path returns the file path being written to.
func (w *Writer) Path() string { return w.path }
