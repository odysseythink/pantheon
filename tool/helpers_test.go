package tool

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestError(t *testing.T) {
	out := Error("not found")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, "not found", decoded["error"])
}

func TestResultWithMap(t *testing.T) {
	out := Result(map[string]any{"ok": true, "count": 3})
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &decoded))
	assert.Equal(t, true, decoded["ok"])
	assert.Equal(t, float64(3), decoded["count"])
}

func TestResultWithString(t *testing.T) {
	out := Result("already encoded")
	assert.Equal(t, "already encoded", out)
}
