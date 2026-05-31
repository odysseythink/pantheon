package compression

import (
	"sync"

	"github.com/odysseythink/pantheon/core"
)

// MemoryProvider receives lifecycle hooks around compression events.
type MemoryProvider interface {
	OnPreCompress(messages []core.Message) ([]core.Message, error)
	OnSessionSwitch(newSessionID, parentSessionID string) error
}

// MemoryProviderRegistry holds zero or more providers.
type MemoryProviderRegistry struct {
	providers []MemoryProvider
	mu        sync.RWMutex
}

// NewMemoryProviderRegistry creates an empty registry.
func NewMemoryProviderRegistry() *MemoryProviderRegistry {
	return &MemoryProviderRegistry{}
}

// Register adds a memory provider.
func (r *MemoryProviderRegistry) Register(p MemoryProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

// OnPreCompress calls all registered providers in order.
func (r *MemoryProviderRegistry) OnPreCompress(messages []core.Message) ([]core.Message, error) {
	r.mu.RLock()
	providers := append([]MemoryProvider(nil), r.providers...)
	r.mu.RUnlock()

	for _, p := range providers {
		var err error
		messages, err = p.OnPreCompress(messages)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

// OnSessionSwitch calls all registered providers in order.
func (r *MemoryProviderRegistry) OnSessionSwitch(newSessionID, parentSessionID string) error {
	r.mu.RLock()
	providers := append([]MemoryProvider(nil), r.providers...)
	r.mu.RUnlock()

	for _, p := range providers {
		if err := p.OnSessionSwitch(newSessionID, parentSessionID); err != nil {
			return err
		}
	}
	return nil
}
