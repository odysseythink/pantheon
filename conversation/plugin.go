package conversation

// Plugin extends a Conversation with additional behavior.
type Plugin interface {
	Name() string
	Setup(conv *Conversation) error
}

// Use installs one or more plugins.
func (c *Conversation) Use(plugins ...Plugin) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range plugins {
		if err := p.Setup(c); err != nil {
			return err
		}
		c.plugins = append(c.plugins, p)
	}
	return nil
}
