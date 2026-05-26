package conversation

// StartHandler is called when a conversation starts.
type StartHandler func(chat Chat, conv *Conversation)

// MessageHandler is called when a new message is added to history.
type MessageHandler func(chat Chat, conv *Conversation)

// ErrorHandler is called when an error occurs during a chat turn.
type ErrorHandler func(err error, route Route, conv *Conversation)

// TerminateHandler is called when the conversation terminates.
type TerminateHandler func(node string, conv *Conversation)

// InterruptHandler is called when the conversation is interrupted.
type InterruptHandler func(route Route, conv *Conversation)

// OnStart registers a handler for the start event.
func (c *Conversation) OnStart(handler StartHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStart = append(c.onStart, handler)
}

// OnMessage registers a handler for the message event.
func (c *Conversation) OnMessage(handler MessageHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMessage = append(c.onMessage, handler)
}

// OnError registers a handler for the error event.
func (c *Conversation) OnError(handler ErrorHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onError = append(c.onError, handler)
}

// OnTerminate registers a handler for the terminate event.
func (c *Conversation) OnTerminate(handler TerminateHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onTerminate = append(c.onTerminate, handler)
}

// OnInterrupt registers a handler for the interrupt event.
func (c *Conversation) OnInterrupt(handler InterruptHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onInterrupt = append(c.onInterrupt, handler)
}

// emitStart fires start handlers (must be called without lock held).
func (c *Conversation) emitStart(chat Chat) {
	c.mu.RLock()
	handlers := make([]StartHandler, len(c.onStart))
	copy(handlers, c.onStart)
	c.mu.RUnlock()
	for _, h := range handlers {
		h(chat, c)
	}
}

// emitMessage fires message handlers (must be called without lock held).
func (c *Conversation) emitMessage(chat Chat) {
	c.mu.RLock()
	handlers := make([]MessageHandler, len(c.onMessage))
	copy(handlers, c.onMessage)
	c.mu.RUnlock()
	for _, h := range handlers {
		h(chat, c)
	}
}

// emitError fires error handlers (must be called without lock held).
func (c *Conversation) emitError(err error, route Route) {
	c.mu.RLock()
	handlers := make([]ErrorHandler, len(c.onError))
	copy(handlers, c.onError)
	c.mu.RUnlock()
	for _, h := range handlers {
		h(err, route, c)
	}
}

// emitTerminate fires terminate handlers (must be called without lock held).
func (c *Conversation) emitTerminate(node string) {
	c.mu.RLock()
	handlers := make([]TerminateHandler, len(c.onTerminate))
	copy(handlers, c.onTerminate)
	c.mu.RUnlock()
	for _, h := range handlers {
		h(node, c)
	}
}

// emitInterrupt fires interrupt handlers (must be called without lock held).
func (c *Conversation) emitInterrupt(route Route) {
	c.mu.RLock()
	handlers := make([]InterruptHandler, len(c.onInterrupt))
	copy(handlers, c.onInterrupt)
	c.mu.RUnlock()
	for _, h := range handlers {
		h(route, c)
	}
}
