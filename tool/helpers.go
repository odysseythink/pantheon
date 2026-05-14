package tool

import (
	"encoding/json"
	"fmt"
)

// Error returns a JSON-encoded tool error string.
func Error(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

// Result returns a JSON-encoded tool success payload.
func Result(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	b, err := json.Marshal(data)
	if err != nil {
		return Error("marshal: " + err.Error())
	}
	return string(b)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newPanicError(toolName string, p any) error {
	return fmt.Errorf("tool %q panicked: %v", toolName, p)
}
