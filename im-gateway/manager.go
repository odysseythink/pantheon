package imgateway

// Channel manager: orchestrates multiple channels with session-aware batching.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PreLockHandler is an optional hook invoked before the per-session lock is
// acquired. Used by hosts to signal /stop //cancel without waiting for the
// in-flight turn.
type PreLockHandler func(ctx context.Context, channelID string, message *InboundMessage)

// MessagePredicate classifies inbound messages (control / interrupt).
type MessagePredicate func(message *InboundMessage) bool

// ChannelFactory builds a channel instance from a config. Registered per
// channel type by the channel packages (mirrors BUILTIN_CHANNELS).
type ChannelFactory func(opts ChannelBuildOptions) (ChannelImpl, error)

// ChannelBuildOptions carries everything a factory needs.
type ChannelBuildOptions struct {
	Processor  MessageProcessor
	Config     any // dict map[string]any or a typed config struct
	ChannelID  string
	TenantID   string
	ExtraKwarg map[string]any // forwarded constructor kwargs (debounce_seconds, constraints, ...)
}

// ManagerOptions configures ChannelManager construction.
type ManagerOptions struct {
	WorkersPerChannel         int
	QueueMaxSize              int
	MediaBackend              MediaBackend
	Processor                 MessageProcessor
	OnPreLock                 PreLockHandler
	ControlMessagePredicate   MessagePredicate
	InterruptMessagePredicate MessagePredicate
}

// ChannelManager orchestrates multiple IM channels with:
//
//   - Per-channel message queues
//   - Configurable worker pool per channel
//   - Session-aware sequential processing (same session → no interleaving)
//   - Cross-session parallelism
//   - Proactive push API
type ChannelManager struct {
	workersPerChannel         int
	queueMaxSize              int
	mediaBackend              MediaBackend
	defaultProcessor          MessageProcessor
	onPreLock                 PreLockHandler
	controlMessagePredicate   MessagePredicate
	interruptMessagePredicate MessagePredicate

	mu           sync.Mutex
	channels     map[string]ChannelImpl
	queues       map[string]*payloadQueue
	sessionLocks map[string]*sessionMutex
	globalCons   map[string]any

	workerWG   sync.WaitGroup
	workersCtx context.Context
	workersMu  sync.Mutex
	running    bool
	stopCancel context.CancelFunc
}

// NewChannelManager builds a manager. workersPerChannel and queueMaxSize
// default to 4 and 1000 when zero.
func NewChannelManager(opts ManagerOptions) *ChannelManager {
	if opts.WorkersPerChannel <= 0 {
		opts.WorkersPerChannel = 4
	}
	if opts.QueueMaxSize <= 0 {
		opts.QueueMaxSize = 1000
	}
	return &ChannelManager{
		workersPerChannel:         opts.WorkersPerChannel,
		queueMaxSize:              opts.QueueMaxSize,
		mediaBackend:              opts.MediaBackend,
		defaultProcessor:          opts.Processor,
		onPreLock:                 opts.OnPreLock,
		controlMessagePredicate:   opts.ControlMessagePredicate,
		interruptMessagePredicate: opts.InterruptMessagePredicate,
		channels:                  map[string]ChannelImpl{},
		queues:                    map[string]*payloadQueue{},
		sessionLocks:              map[string]*sessionMutex{},
		globalCons:                map[string]any{},
	}
}

// SetPreLockHandler installs or clears the pre-session-lock inbound hook.
func (m *ChannelManager) SetPreLockHandler(handler PreLockHandler) {
	m.onPreLock = handler
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts all channels and their consumer worker loops.
func (m *ChannelManager) Start(ctx context.Context) error {
	m.workersMu.Lock()
	if m.running {
		m.workersMu.Unlock()
		return nil
	}
	m.running = true
	workersCtx, cancel := context.WithCancel(ctx)
	m.workersCtx = workersCtx
	m.stopCancel = cancel
	m.workersMu.Unlock()

	// Fall back to a filesystem backend rooted at "/" when the caller did
	// not supply one — mirrors the Python behavior.
	if m.mediaBackend == nil {
		m.mediaBackend = NewFileSystemMediaBackend("/")
	}

	m.mu.Lock()
	channels := make(map[string]ChannelImpl, len(m.channels))
	for id, ch := range m.channels {
		channels[id] = ch
	}
	m.mu.Unlock()

	for _, ch := range channels {
		if base := baseOf(ch); base != nil {
			base.SetMediaBackend(m.mediaBackend)
		}
	}
	m.applyGlobalConstraints()

	// Initialize queues and set enqueue callbacks
	for id, ch := range channels {
		m.mu.Lock()
		if m.queues[id] == nil {
			m.queues[id] = newPayloadQueue(m.queueMaxSize)
		}
		m.mu.Unlock()
		cid := id
		if base := baseOf(ch); base != nil {
			base.SetEnqueueCallback(func(payload any) { m.Enqueue(cid, payload) })
		}
	}

	// Start all channels
	for id, ch := range channels {
		if err := ch.Start(ctx); err != nil {
			logError("failed to start channel: %s err=%v", id, err)
		} else {
			logInfo("channel started: %s", id)
		}
	}

	// Spawn workers
	for id := range channels {
		for i := 0; i < m.workersPerChannel; i++ {
			m.workerWG.Add(1)
			go func(cid string) {
				defer m.workerWG.Done()
				m.workerLoop(workersCtx, cid)
			}(id)
		}
	}

	logInfo("ChannelManager started: %d channels, %d workers each", len(channels), m.workersPerChannel)
	return nil
}

// Stop stops all channels and cancels worker tasks.
func (m *ChannelManager) Stop() {
	m.workersMu.Lock()
	if !m.running {
		m.workersMu.Unlock()
		return
	}
	m.running = false
	if m.stopCancel != nil {
		m.stopCancel()
	}
	m.workersMu.Unlock()

	m.workerWG.Wait()

	m.mu.Lock()
	channels := make(map[string]ChannelImpl, len(m.channels))
	for id, ch := range m.channels {
		channels[id] = ch
	}
	m.mu.Unlock()

	for id, ch := range channels {
		if err := ch.Stop(context.Background()); err != nil {
			logError("failed to stop channel: %s err=%v", id, err)
		} else {
			logInfo("channel stopped: %s", id)
		}
	}

	m.mu.Lock()
	m.queues = map[string]*payloadQueue{}
	m.sessionLocks = map[string]*sessionMutex{}
	m.mu.Unlock()
	logInfo("ChannelManager stopped")
}

// Running reports whether the manager is started.
func (m *ChannelManager) Running() bool {
	m.workersMu.Lock()
	defer m.workersMu.Unlock()
	return m.running
}

// ---------------------------------------------------------------------------
// Constraints
// ---------------------------------------------------------------------------

// SetConstraints updates constraints on ALL channels at runtime. Only known
// ChannelConstraints fields are applied; unknown keys are ignored.
func (m *ChannelManager) SetConstraints(kwargs map[string]any) {
	m.mu.Lock()
	for k, v := range kwargs {
		m.globalCons[k] = v
	}
	m.mu.Unlock()
	m.applyGlobalConstraints()
}

func (m *ChannelManager) applyGlobalConstraints() {
	m.mu.Lock()
	overrides := make(map[string]any, len(m.globalCons))
	for k, v := range m.globalCons {
		overrides[k] = v
	}
	channels := make([]ChannelImpl, 0, len(m.channels))
	for _, ch := range m.channels {
		channels = append(channels, ch)
	}
	m.mu.Unlock()

	for _, ch := range channels {
		base := baseOf(ch)
		if base == nil {
			continue
		}
		applyConstraintOverrides(base.Constraints(), overrides)
	}
}

func (m *ChannelManager) applyGlobalConstraintsTo(ch ChannelImpl) {
	m.mu.Lock()
	overrides := make(map[string]any, len(m.globalCons))
	for k, v := range m.globalCons {
		overrides[k] = v
	}
	m.mu.Unlock()
	if base := baseOf(ch); base != nil {
		applyConstraintOverrides(base.Constraints(), overrides)
	}
}

func baseOf(ch ChannelImpl) *BaseChannel {
	if ch == nil {
		return nil
	}
	// Channels embed *BaseChannel; recover it through the Base() accessor.
	if b, ok := ch.(interface{ Base() *BaseChannel }); ok {
		return b.Base()
	}
	return nil
}

func applyConstraintOverrides(c *ChannelConstraints, overrides map[string]any) {
	for key, value := range overrides {
		switch key {
		case "reply_timeout":
			if f, ok := toFloatAny(value); ok {
				c.ReplyTimeout = f
			}
		case "send_rate_limit":
			if pair, ok := value.([2]float64); ok {
				c.SendRateLimitMax = int(pair[0])
				c.SendRateLimitWindow = pair[1]
			}
		case "typing_keepalive_interval":
			if f, ok := toFloatAny(value); ok {
				c.TypingKeepaliveInterval = f
			}
		case "timeout_strategy":
			if s, ok := value.(string); ok {
				c.TimeoutStrategy = s
			}
		case "placeholder_texts":
			if list, ok := value.([]string); ok {
				c.PlaceholderTexts = list
			}
		case "tool_hint_throttle":
			if f, ok := toFloatAny(value); ok {
				c.ToolHintThrottle = f
			}
		case "show_tool_hints":
			if b, ok := value.(bool); ok {
				c.ShowToolHints = b
			}
		case "tool_hint_template":
			if s, ok := value.(string); ok {
				c.ToolHintTemplate = s
			}
		case "tool_end_template":
			if s, ok := value.(string); ok {
				c.ToolEndTemplate = s
			}
		case "show_thinking":
			if b, ok := value.(bool); ok {
				c.ShowThinking = b
			}
		case "thinking_template":
			if s, ok := value.(string); ok {
				c.ThinkingTemplate = s
			}
		}
	}
}

func toFloatAny(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Enqueue
// ---------------------------------------------------------------------------

// Enqueue enqueues a raw payload for processing by the channel's workers.
// Safe for concurrent use from any goroutine (mirrors the thread-safe
// call_soon_threadsafe path).
func (m *ChannelManager) Enqueue(channelID string, payload any) {
	m.mu.Lock()
	q := m.queues[channelID]
	m.mu.Unlock()
	if q == nil {
		logWarn("no queue for channel %s, dropping payload", channelID)
		return
	}

	// Interrupt fast-path: urgent control messages bypass the session lock.
	if m.interruptMessagePredicate != nil {
		base := baseOfChannel(m, channelID)
		if base != nil {
			message, err := base.ParseInbound(context.Background(), payload)
			if err == nil && message != nil && m.interruptMessagePredicate(message) {
				m.workersMu.Lock()
				wctx := m.workersCtx
				m.workersMu.Unlock()
				go m.runInterrupt(wctx, channelID, base, message)
				return
			}
		}
	}

	if !q.Put(payload) {
		logError("queue full for channel %s, dropping payload", channelID)
	}
}

func baseOfChannel(m *ChannelManager, channelID string) *BaseChannel {
	m.mu.Lock()
	ch := m.channels[channelID]
	m.mu.Unlock()
	return baseOf(ch)
}

func (m *ChannelManager) runInterrupt(ctx context.Context, channelID string, base *BaseChannel, message *InboundMessage) {
	defer func() {
		if r := recover(); r != nil {
			logError("interrupt worker error: channel=%s panic=%v", channelID, r)
		}
	}()
	if err := base.HandleInbound(ctx, message); err != nil {
		logError("interrupt worker error: channel=%s err=%v", channelID, err)
	}
}

// ---------------------------------------------------------------------------
// Proactive push API
// ---------------------------------------------------------------------------

// PushText pushes a text message proactively via a specific channel.
func (m *ChannelManager) PushText(ctx context.Context, channelID string, subject *ChannelSubject, text string) error {
	base := baseOfChannel(m, channelID)
	if base == nil {
		return fmt.Errorf("channel not found: %s", channelID)
	}
	return base.PushText(ctx, subject, text)
}

// PushContent pushes rich content proactively via a specific channel.
func (m *ChannelManager) PushContent(ctx context.Context, channelID string, subject *ChannelSubject, parts []ContentPart) error {
	base := baseOfChannel(m, channelID)
	if base == nil {
		return fmt.Errorf("channel not found: %s", channelID)
	}
	return base.PushMessage(ctx, subject, parts)
}

// RunInSession runs a proactive operation under the inbound session lock.
func (m *ChannelManager) RunInSession(ctx context.Context, channelID, sessionKey string, operation func(ctx context.Context) error) error {
	m.mu.Lock()
	if _, ok := m.channels[channelID]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("channel not found: %s", channelID)
	}
	m.mu.Unlock()
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return errors.New("session_key must not be empty")
	}
	unlock := m.sessionLock(channelID, key)
	defer unlock()
	return operation(ctx)
}

// ---------------------------------------------------------------------------
// Channel management
// ---------------------------------------------------------------------------

// GetChannel gets a channel by its ID.
func (m *ChannelManager) GetChannel(channelID string) ChannelImpl {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.channels[channelID]
}

// Register registers a constructed channel instance, starts it when the
// manager is running, and spawns workers. Returns the channel ID.
func (m *ChannelManager) Register(channel ChannelImpl) (string, error) {
	base := baseOf(channel)
	if base == nil {
		return "", errors.New("channel must embed BaseChannel")
	}
	channelID := base.ChannelID()

	m.mu.Lock()
	if _, exists := m.channels[channelID]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("channel already exists: %s", channelID)
	}
	m.channels[channelID] = channel
	m.queues[channelID] = newPayloadQueue(m.queueMaxSize)
	m.mu.Unlock()

	cid := channelID
	base.SetEnqueueCallback(func(payload any) { m.Enqueue(cid, payload) })

	if m.mediaBackend != nil {
		base.SetMediaBackend(m.mediaBackend)
	}
	m.applyGlobalConstraintsTo(channel)

	m.workersMu.Lock()
	running := m.running
	wctx := m.workersCtx
	m.workersMu.Unlock()

	if running {
		if err := channel.Start(wctx); err != nil {
			// A failed login must not leave a registered, workerless channel.
			m.mu.Lock()
			delete(m.channels, channelID)
			delete(m.queues, channelID)
			m.mu.Unlock()
			_ = channel.Stop(context.Background())
			return "", err
		}
		for i := 0; i < m.workersPerChannel; i++ {
			m.workerWG.Add(1)
			go func(id string) {
				defer m.workerWG.Done()
				m.workerLoop(wctx, id)
			}(channelID)
		}
	}

	logInfo("channel registered: id=%s type=%s tenant=%s", channelID, base.ChannelType(), base.TenantID())
	return channelID, nil
}

// RemoveChannel dynamically removes a channel (stops it and cancels its
// workers by closing the queue).
func (m *ChannelManager) RemoveChannel(channelID string) error {
	m.mu.Lock()
	channel, ok := m.channels[channelID]
	if ok {
		delete(m.channels, channelID)
	}
	q := m.queues[channelID]
	if ok {
		delete(m.queues, channelID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	if q != nil {
		q.Close()
	}
	return channel.Stop(context.Background())
}

// ChannelIDs returns the list of registered channel IDs.
func (m *ChannelManager) ChannelIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.channels))
	for id := range m.channels {
		ids = append(ids, id)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Worker loop
// ---------------------------------------------------------------------------

func (m *ChannelManager) workerLoop(ctx context.Context, channelID string) {
	m.mu.Lock()
	q := m.queues[channelID]
	ch := m.channels[channelID]
	m.mu.Unlock()
	if q == nil || ch == nil {
		return
	}
	base := baseOf(ch)
	if base == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, ok := q.Get(ctx, time.Second)
		if !ok {
			continue
		}

		func() {
			defer func() {
				if r := recover(); r != nil {
					// One malformed payload or adapter failure must not stop
					// the worker.
					logError("worker error: channel=%s panic=%v", channelID, r)
				}
			}()

			// Parse to get session key for locking
			message, err := payloadToMessage(ctx, base, payload)
			if err != nil {
				logError("worker error: channel=%s parse err=%v", channelID, err)
				return
			}
			sessionKey := base.debounceKey(message)

			// Drain additional same-session items from queue
			batch := []*InboundMessage{message}
			isControl := m.controlMessagePredicate != nil && m.controlMessagePredicate(message)
			if !isControl && base.shouldBatchInbound(message) {
				batch = append(batch, m.drainSameSession(ctx, q, base, sessionKey)...)
			}

			// Merge batch if multiple
			var merged *InboundMessage
			if len(batch) > 1 {
				merged = base.mergeInbound(batch)
			} else {
				merged = batch[0]
			}

			// Allow hosts to act before the session lock — e.g. signal cancel
			// for /stop so the in-flight turn can release the lock.
			if m.onPreLock != nil {
				func() {
					defer func() {
						if r := recover(); r != nil {
							logError("pre-lock handler error: channel=%s panic=%v", channelID, r)
						}
					}()
					m.onPreLock(ctx, channelID, merged)
				}()
			}

			// Acquire session lock to prevent interleaving. Native IDs are
			// only unique inside one registered channel; prefix the shared
			// lock registry to avoid cross-account or cross-platform
			// collisions.
			unlock := m.sessionLock(channelID, sessionKey)
			defer unlock()
			if err := base.HandleInbound(ctx, merged); err != nil {
				logError("worker error: channel=%s handle err=%v", channelID, err)
			}
		}()
	}
}

func payloadToMessage(ctx context.Context, base *BaseChannel, payload any) (*InboundMessage, error) {
	if msg, ok := payload.(*InboundMessage); ok {
		return msg, nil
	}
	return base.ParseInbound(ctx, payload)
}

// sessionLock returns the shared lock for one registered-channel session.
func (m *ChannelManager) sessionLock(channelID, sessionKey string) func() {
	lockKey := fmt.Sprintf("%s:%s", channelID, sessionKey)
	m.mu.Lock()
	sm, ok := m.sessionLocks[lockKey]
	if !ok {
		sm = &sessionMutex{}
		m.sessionLocks[lockKey] = sm
	}
	m.mu.Unlock()
	sm.Lock()
	return sm.Unlock
}

// drainSameSession non-blockingly drains same-session items from the queue.
func (m *ChannelManager) drainSameSession(ctx context.Context, q *payloadQueue, base *BaseChannel, sessionKey string) []*InboundMessage {
	var drained []*InboundMessage
	const maxDrain = 20 // Prevent unbounded draining

	for i := 0; i < maxDrain; i++ {
		payload, ok := q.GetNowait()
		if !ok {
			break
		}

		msg, err := payloadToMessage(ctx, base, payload)
		if err != nil {
			// Parsing is adapter-defined; stop draining and preserve queued
			// payloads.
			logError("failed to parse payload during drain err=%v", err)
			break
		}
		if m.controlMessagePredicate != nil && m.controlMessagePredicate(msg) {
			if !q.Put(payload) {
				logWarn("queue full when re-enqueuing control message, dropping")
			}
			break
		}
		if base.debounceKey(msg) == sessionKey {
			drained = append(drained, msg)
		} else {
			// Different session, put it back
			if !q.Put(payload) {
				logWarn("queue full when re-enqueuing, dropping")
			}
			break
		}
	}
	return drained
}

// ---------------------------------------------------------------------------
// User Registry
// ---------------------------------------------------------------------------

// ListSubjects lists known subjects for a specific channel.
func (m *ChannelManager) ListSubjects(channelID string) []*ChannelSubject {
	base := baseOfChannel(m, channelID)
	if base == nil {
		return nil
	}
	return base.ListSubjects()
}

// ListAllSubjects lists known subjects across all channels.
func (m *ChannelManager) ListAllSubjects() map[string][]*ChannelSubject {
	m.mu.Lock()
	channels := make(map[string]*BaseChannel, len(m.channels))
	for id, ch := range m.channels {
		if b := baseOf(ch); b != nil {
			channels[id] = b
		}
	}
	m.mu.Unlock()
	out := make(map[string][]*ChannelSubject, len(channels))
	for id, b := range channels {
		out[id] = b.ListSubjects()
	}
	return out
}

// PushToAll pushes a text message to all known subjects of a channel.
func (m *ChannelManager) PushToAll(ctx context.Context, channelID string, text string) error {
	base := baseOfChannel(m, channelID)
	if base == nil {
		return fmt.Errorf("channel not found: %s", channelID)
	}
	for _, subject := range base.ListSubjects() {
		if err := base.PushText(ctx, subject, text); err != nil {
			// Continue fan-out when one platform subject rejects a push.
			logWarn("failed to push to subject %s on %s", subject.SubjectID, channelID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Registry (mirrors channels/__init__.py BUILTIN_CHANNELS)
// ---------------------------------------------------------------------------

var (
	registryMu       sync.Mutex
	channelFactories = map[string]ChannelFactory{}
)

// SupportedChannelKinds mirrors SUPPORTED_CHANNEL_KINDS. Populated by the
// channel packages' init() via RegisterChannel.
var SupportedChannelKinds = map[string]bool{}

// RegisterChannel registers a built-in channel factory under the given kind.
// Called from channel package init() functions; the host blank-imports the
// channel packages it needs (or the convenience package imgateway/all).
func RegisterChannel(kind string, factory ChannelFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	channelFactories[kind] = factory
	SupportedChannelKinds[kind] = true
}

// RegisteredKinds returns the sorted list of registered channel kinds.
func RegisteredKinds() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	kinds := make([]string, 0, len(channelFactories))
	for k := range channelFactories {
		kinds = append(kinds, k)
	}
	// insertion order not preserved by map; sort for stable output
	for i := 1; i < len(kinds); i++ {
		for j := i; j > 0 && kinds[j] < kinds[j-1]; j-- {
			kinds[j], kinds[j-1] = kinds[j-1], kinds[j]
		}
	}
	return kinds
}

// AddChannel adds a channel dynamically by type (config-based, recommended).
//
//	config := map[string]any{"app_id": "cli_xxx", "app_secret": "yyy"}
//	channelID, err := manager.AddChannel(ctx, "feishu", config, AddOptions{TenantID: "acme"})
func (m *ChannelManager) AddChannel(ctx context.Context, channelType string, config any, opts AddOptions) (string, error) {
	registryMu.Lock()
	factory, ok := channelFactories[channelType]
	registryMu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown channel_type '%s'. available: %v", channelType, RegisteredKinds())
	}

	processor := opts.Processor
	if processor == nil {
		processor = m.defaultProcessor
	}
	if processor == nil {
		return "", errors.New("no processor available. either pass AddOptions.Processor or set ManagerOptions.Processor")
	}

	// Typed configs self-report missing credentials (dict configs are
	// checked inside the factory after FromDict conversion).
	if cc, ok := config.(CredentialChecker); ok {
		if missing := cc.MissingCredentials(); len(missing) > 0 {
			return "", &ChannelCredentialsError{Kind: channelType, Missing: missing}
		}
	}

	buildOpts := ChannelBuildOptions{
		Processor:  processor,
		Config:     config,
		ChannelID:  opts.ChannelID,
		TenantID:   opts.TenantID,
		ExtraKwarg: opts.Extra,
	}
	channel, err := factory(buildOpts)
	if err != nil {
		return "", err
	}
	if _, err := m.Register(channel); err != nil {
		_ = channel.Stop(ctx)
		return "", err
	}
	return baseOf(channel).ChannelID(), nil
}

// AddOptions carries AddChannel keyword options.
type AddOptions struct {
	ChannelID string
	TenantID  string
	Processor MessageProcessor
	Extra     map[string]any
}

// ProbeChannel starts/stops an ephemeral channel instance to verify
// credentials. Yuanbao probes default to sign_token mode (a second full
// WebSocket session would kick the live channel offline).
func (m *ChannelManager) ProbeChannel(ctx context.Context, channelType string, config any, tenantID string) error {
	registryMu.Lock()
	factory, ok := channelFactories[channelType]
	registryMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown channel_type '%s'. available: %v", channelType, RegisteredKinds())
	}
	processor := m.defaultProcessor
	if processor == nil {
		return errors.New("no processor available. set ManagerOptions.Processor before ProbeChannel")
	}

	probeConfig := config
	if channelType == "yuanbao" {
		if cfgMap, ok := config.(map[string]any); ok {
			if _, has := cfgMap["probe_mode"]; !has {
				cp := make(map[string]any, len(cfgMap)+1)
				for k, v := range cfgMap {
					cp[k] = v
				}
				cp["probe_mode"] = "sign_token"
				probeConfig = cp
			}
		}
	}

	// Typed configs self-report missing credentials (dict configs are
	// checked inside the factory after FromDict conversion).
	if cc, ok := probeConfig.(CredentialChecker); ok {
		if missing := cc.MissingCredentials(); len(missing) > 0 {
			return &ChannelCredentialsError{Kind: channelType, Missing: missing}
		}
	}

	channel, err := factory(ChannelBuildOptions{
		Processor: processor,
		Config:    probeConfig,
		ChannelID: "__probe__",
		TenantID:  tenantID,
	})
	if err != nil {
		return err
	}
	if base := baseOf(channel); base != nil {
		if m.mediaBackend != nil {
			base.SetMediaBackend(m.mediaBackend)
		}
	}
	if err := channel.Start(ctx); err != nil {
		_ = channel.Stop(context.Background())
		return err
	}
	return channel.Stop(ctx)
}

// missingCredentials inspects dict configs for required credential fields.
// Typed configs implement the CredentialChecker contract themselves.
func missingCredentials(config any) []string {
	if cc, ok := config.(CredentialChecker); ok {
		return cc.MissingCredentials()
	}
	return nil
}

// CredentialChecker is implemented by config values that can self-report
// missing required credentials (mirrors ChannelConfig.missing_credentials).
type CredentialChecker interface {
	MissingCredentials() []string
}

// ---------------------------------------------------------------------------
// Payload queue: a bounded FIFO with put-back support (mirrors asyncio.Queue
// with maxsize + the worker drain re-enqueue behavior).
// ---------------------------------------------------------------------------

type payloadQueue struct {
	mu      sync.Mutex
	items   []any
	maxSize int
	notify  chan struct{}
	closed  bool
}

func newPayloadQueue(maxSize int) *payloadQueue {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &payloadQueue{
		maxSize: maxSize,
		notify:  make(chan struct{}, maxSize),
	}
}

// Put pushes an item (non-blocking). Returns false when full or closed.
func (q *payloadQueue) Put(item any) bool {
	q.mu.Lock()
	if q.closed || len(q.items) >= q.maxSize {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, item)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return true
}

// Get blocks up to timeout for one item. Returns ok=false on timeout/close.
func (q *payloadQueue) Get(ctx context.Context, timeout time.Duration) (any, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return nil, false
		}

		timer := time.NewTimer(timeout)
		select {
		case <-q.notify:
			timer.Stop()
			// loop to re-check items
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
			return nil, false
		}
	}
}

// GetNowait returns one item without blocking.
func (q *payloadQueue) GetNowait() (any, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	return item, true
}

// Close marks the queue closed; blocked Get calls finish their wait and
// return false.
func (q *payloadQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Keyed session mutex
// ---------------------------------------------------------------------------

type sessionMutex struct {
	mu sync.Mutex
}

func (s *sessionMutex) Lock()    { s.mu.Lock() }
func (s *sessionMutex) Unlock()  { s.mu.Unlock() }
