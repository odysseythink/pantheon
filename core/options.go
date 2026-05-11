package core

import (
	"encoding/json"
)

// ProviderOptionsDataer is implemented by provider-specific option types.
type ProviderOptionsDataer interface {
	ProviderName() string
}

// ProviderOptions holds provider-specific options keyed by provider name.
type ProviderOptions map[string]ProviderOptionsDataer

// Get retrieves provider-specific options by provider name.
func (po ProviderOptions) Get(name string) (ProviderOptionsDataer, bool) {
	v, ok := po[name]
	return v, ok
}

// Set stores provider-specific options under the given provider name.
func (po ProviderOptions) Set(name string, opts ProviderOptionsDataer) {
	po[name] = opts
}

// MarshalJSON serializes ProviderOptions to JSON.
func (po ProviderOptions) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(po))
	for k, v := range po {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = data
	}
	return json.Marshal(m)
}

// UnmarshalJSON deserializes ProviderOptions from JSON.
func (po *ProviderOptions) UnmarshalJSON(data []byte) error {
	// Provider-specific deserialization is handled by each provider individually.
	// This no-op allows Request/ObjectRequest JSON round-trips without breaking.
	return nil
}
