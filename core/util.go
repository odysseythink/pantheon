package core

import "encoding/json"

// ParseOptions parses a map[string]any into a struct using JSON tags.
// It is the pantheon equivalent of fantasy.ParseOptions.
func ParseOptions(data map[string]any, dst any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
