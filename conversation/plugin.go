package conversation

// Plugin extends a Conversation with additional behavior.
type Plugin interface {
	Name() string
	Setup(conv *Conversation) error
}

// Use installs one or more plugins.
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
