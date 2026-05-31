package compression

import (
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// EngineFactory creates a ContextEngine from config + aux model.
type EngineFactory func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error)

// EngineRegistry holds discovered engine implementations.
type EngineRegistry struct {
	engines map[string]EngineFactory
}

// NewEngineRegistry creates an empty registry.
func NewEngineRegistry() *EngineRegistry {
	return &EngineRegistry{
		engines: make(map[string]EngineFactory),
	}
}

// Register adds an engine factory. Panics on duplicate name.
func (r *EngineRegistry) Register(name string, factory EngineFactory) {
	if _, exists := r.engines[name]; exists {
		panic(fmt.Sprintf("compression engine %q already registered", name))
	}
	r.engines[name] = factory
}

// Create instantiates an engine by name. Returns error if not found.
func (r *EngineRegistry) Create(name string, cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error) {
	factory, ok := r.engines[name]
	if !ok {
		return nil, fmt.Errorf("compression engine %q not found", name)
	}
	return factory(cfg, aux)
}

// Names returns all registered engine names.
func (r *EngineRegistry) Names() []string {
	names := make([]string, 0, len(r.engines))
	for n := range r.engines {
		names = append(names, n)
	}
	return names
}

// DefaultRegistry is the global registry with the built-in engine pre-registered.
var DefaultRegistry = NewEngineRegistry()

func init() {
	DefaultRegistry.Register("default", func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error) {
		return NewDefaultCompressor(cfg, aux), nil
	})
}
