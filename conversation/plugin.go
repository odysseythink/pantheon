package conversation

// Plugin extends a Conversation with additional behavior.
type Plugin interface {
	Name() string
	Setup(conv *Conversation) error
}

// Use installs one or more plugins.
// If any plugin's Setup fails, previously installed plugins from this call
// remain active (their handlers are already registered).
func (c *Conversation) Use(plugins ...Plugin) error {
	for _, p := range plugins {
		if err := p.Setup(c); err != nil {
			return err
		}
		c.mu.Lock()
		c.plugins = append(c.plugins, p)
		c.mu.Unlock()
	}
	return nil
}

// Plugins returns the list of installed plugins.
func (c *Conversation) Plugins() []Plugin {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Plugin, len(c.plugins))
	copy(out, c.plugins)
	return out
}
