package core

import "encoding/json"

type ProviderOptionsDataer interface {
	ProviderName() string
}

type ProviderOptions map[string]ProviderOptionsDataer

func (po ProviderOptions) Get(name string) (ProviderOptionsDataer, bool) {
	v, ok := po[name]
	return v, ok
}

func (po ProviderOptions) Set(name string, opts ProviderOptionsDataer) {
	po[name] = opts
}

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

func (po ProviderOptions) UnmarshalJSON(data []byte) error {
	return nil
}
