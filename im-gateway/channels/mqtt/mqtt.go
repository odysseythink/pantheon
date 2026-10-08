// Package mqtt ports octop_gateway.channels.mqtt: MQTT channel for IoT
// devices and robots, using paho.mqtt.golang in background goroutines.
package mqtt

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// MQTTConfig is the configuration for the MQTT channel (mirrors MQTTConfig).
type MQTTConfig struct {
	imgateway.ChannelConfig

	// Host is the MQTT broker hostname.
	Host string `json:"host,omitempty"`
	// Port is the broker port (8883 for MQTT over TLS).
	Port int `json:"port,omitempty"`
	// Transport is the paho transport — "tcp" or "websockets".
	Transport string `json:"transport,omitempty"`
	// Username is the optional broker username.
	Username string `json:"username,omitempty"`
	// Password is the optional broker password.
	Password string `json:"password,omitempty"`
	// SubscribeTopic is the topic pattern to subscribe for inbound messages.
	SubscribeTopic string `json:"subscribe_topic,omitempty"`
	// PublishTopic is the outbound topic template; "{client_id}" is substituted.
	PublishTopic string `json:"publish_topic,omitempty"`
	// CleanSession is the MQTT clean-session flag.
	CleanSession bool `json:"clean_session,omitempty"`
	// QoS is the QoS level (0, 1, or 2).
	QoS int `json:"qos,omitempty"`
	// TLSEnabled enables TLS for the broker connection.
	TLSEnabled bool `json:"tls_enabled,omitempty"`
	// TLSCaPem is a PEM-encoded CA certificate string (inline).
	TLSCaPem string `json:"tls_ca_pem,omitempty"`
	// TLSCaCerts is the path to a CA certificate file.
	TLSCaCerts string `json:"tls_ca_certs,omitempty"`
	// TLSCertfile is the optional client certificate file.
	TLSCertfile string `json:"tls_certfile,omitempty"`
	// TLSKeyfile is the optional client private key file.
	TLSKeyfile string `json:"tls_keyfile,omitempty"`
}

// NewMQTTConfig returns a config with defaults (mirrors the dataclass defaults).
func NewMQTTConfig() *MQTTConfig {
	return &MQTTConfig{
		Port:           8883,
		Transport:      "tcp",
		SubscribeTopic: "devices/+/in",
		PublishTopic:   "devices/{client_id}/out",
		CleanSession:   true,
		QoS:            2,
		TLSEnabled:     true,
	}
}

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *MQTTConfig) FromDict(data map[string]any) error {
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.Host = imgateway.Str(data, "host", c.Host)
	c.Port = imgateway.Int(data, "port", c.Port)
	c.Transport = imgateway.Str(data, "transport", c.Transport)
	c.Username = imgateway.Str(data, "username", c.Username)
	c.Password = imgateway.Str(data, "password", c.Password)
	c.SubscribeTopic = imgateway.Str(data, "subscribe_topic", c.SubscribeTopic)
	c.PublishTopic = imgateway.Str(data, "publish_topic", c.PublishTopic)
	c.CleanSession = imgateway.Bool(data, "clean_session", c.CleanSession)
	c.QoS = imgateway.Int(data, "qos", c.QoS)
	c.TLSEnabled = imgateway.Bool(data, "tls_enabled", c.TLSEnabled)
	c.TLSCaPem = imgateway.Str(data, "tls_ca_pem", c.TLSCaPem)
	c.TLSCaCerts = imgateway.Str(data, "tls_ca_certs", c.TLSCaCerts)
	c.TLSCertfile = imgateway.Str(data, "tls_certfile", c.TLSCertfile)
	c.TLSKeyfile = imgateway.Str(data, "tls_keyfile", c.TLSKeyfile)
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (mirrors required_credentials = ("host",)).
func (c *MQTTConfig) MissingCredentials() []string {
	var missing []string
	if c.Host == "" {
		missing = append(missing, "host")
	}
	return missing
}

// MQTTChannel is the MQTT channel using paho-mqtt (mirrors MQTTChannel).
type MQTTChannel struct {
	*imgateway.BaseChannel

	config *MQTTConfig

	mu                sync.Mutex
	client            pahomqtt.Client
	connected         bool
	connectionSession string
}

// NewMQTTChannel builds the channel (mirrors MQTTChannel.__init__).
func NewMQTTChannel(processor imgateway.MessageProcessor, config *MQTTConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *MQTTChannel {
	ch := &MQTTChannel{
		config: config,
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "mqtt",
		Processor:       processor,
		ChannelID:       channelID,
		TenantID:        tenantID,
		DebounceSeconds: debounceSeconds,
		Constraints:     constraints,
		Config:          &config.ChannelConfig,
	}, ch)
	return ch
}

// Base exposes the embedded BaseChannel (core manager contract).
func (c *MQTTChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/mqtt "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("mqtt", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewMQTTConfig()
		switch t := opts.Config.(type) {
		case *MQTTConfig:
			cfg = t
		case MQTTConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for MQTTChannel: %w", err)
			}
		}
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "mqtt", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		if opts.ExtraKwarg != nil {
			if c, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = c
			}
		}
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewMQTTChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// DefaultConstraints mirrors _default_constraints.
func (c *MQTTChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	return &imgateway.ChannelConstraints{
		SendRateLimitMax:    30,
		SendRateLimitWindow: 60.0,
		ShowThinking:        false,
		ShowToolHints:       true,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start connects to the MQTT broker and subscribes (mirrors start).
func (c *MQTTChannel) Start(_ context.Context) error {
	if c.config.Host == "" {
		return errors.New("MQTTChannel: host is required")
	}
	if c.config.SubscribeTopic == "" || c.config.PublishTopic == "" {
		return errors.New("MQTTChannel: subscribe_topic and publish_topic are required")
	}

	clientID := "harness-mqtt-" + imgateway.NewUUIDHex()[:12]
	c.mu.Lock()
	c.connectionSession = clientID
	c.mu.Unlock()

	scheme := "tcp"
	if c.config.Transport == "websockets" {
		if c.config.TLSEnabled {
			scheme = "wss"
		} else {
			scheme = "ws"
		}
	}
	broker := fmt.Sprintf("%s://%s:%d", scheme, c.config.Host, c.config.Port)

	opts := pahomqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetProtocolVersion(4). // mqtt.MQTTv311
		SetCleanSession(c.config.CleanSession).
		SetKeepAlive(60 * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(10 * time.Second) // reconnect_delay_set(min_delay=1, max_delay=10)

	if c.config.Username != "" {
		opts.SetUsername(c.config.Username)
		opts.SetPassword(c.config.Password)
	}

	if c.config.TLSEnabled {
		tlsCfg, err := c.buildTLSConfig()
		if err != nil {
			return err
		}
		opts.SetTLSConfig(tlsCfg)
	}

	opts.SetOnConnectHandler(c.onConnect)
	opts.SetConnectionLostHandler(c.onConnectionLost)
	// Paho invokes the publish handler on its network goroutine; dispatch to
	// a dedicated goroutine to isolate malformed messages.
	opts.SetDefaultPublishHandler(func(_ pahomqtt.Client, msg pahomqtt.Message) {
		go c.handleMessage(msg)
	})

	client := pahomqtt.NewClient(opts)
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()

	token := client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		logf("ERROR", "MQTT connect failed: %s:%s", c.config.Host, c.config.Port)
		return err
	}

	logf("INFO", "MQTTChannel started (host=%s port=%s tls=%s)", c.config.Host, c.config.Port, c.config.TLSEnabled)
	return nil
}

// Stop disconnects from the MQTT broker (mirrors stop).
func (c *MQTTChannel) Stop(_ context.Context) error {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		client.Disconnect(0)
	}
	c.mu.Lock()
	c.connected = false
	c.connectionSession = ""
	c.mu.Unlock()
	logf("INFO", "MQTTChannel stopped")
	return nil
}

// buildTLSConfig mirrors the ssl.create_default_context() based TLS setup.
func (c *MQTTChannel) buildTLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case c.config.TLSCaPem != "":
		pool, err := certPoolFromPEM([]byte(c.config.TLSCaPem))
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	case c.config.TLSCaCerts != "":
		pemBytes, err := os.ReadFile(c.config.TLSCaCerts)
		if err != nil {
			return nil, err
		}
		pool, err := certPoolFromPEM(pemBytes)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = pool
	}
	if c.config.TLSCertfile != "" {
		cert, err := tls.LoadX509KeyPair(c.config.TLSCertfile, c.config.TLSKeyfile)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

func certPoolFromPEM(pemBytes []byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("failed to parse CA certificate PEM")
	}
	return pool, nil
}

// ---------------------------------------------------------------------------
// Paho callbacks (run on paho's network goroutines)
// ---------------------------------------------------------------------------

func (c *MQTTChannel) onConnect(client pahomqtt.Client) {
	c.mu.Lock()
	c.connected = true
	c.mu.Unlock()
	client.Subscribe(c.config.SubscribeTopic, byte(c.config.QoS), nil)
	logf("INFO", "MQTT connected, subscribed to %s", c.config.SubscribeTopic)
}

func (c *MQTTChannel) onConnectionLost(_ pahomqtt.Client, err error) {
	c.mu.Lock()
	c.connected = false
	c.mu.Unlock()
	logf("WARN", "MQTT disconnected unexpectedly, code=%s", err)
}

// handleMessage mirrors _on_message (paho sync thread → goroutine).
func (c *MQTTChannel) handleMessage(msg pahomqtt.Message) {
	defer func() {
		if r := recover(); r != nil {
			// Paho invokes this callback on its network goroutine; isolate
			// malformed messages.
			logf("ERROR", "MQTT message handler error panic=%v", r)
		}
	}()

	payload := strings.TrimSpace(string(msg.Payload()))
	content := payload
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err == nil {
		if data == nil {
			// JSON payload that is not an object (e.g. a bare string or
			// list) would raise in Python's data.get(...) — treat as error.
			logf("ERROR", "MQTT message handler error payload is not an object")
			return
		}
		content = strOf(data["text"], "")
	} else {
		content = payload
	}

	if content == "" {
		logf("DEBUG", "MQTT empty message on %s", msg.Topic())
		return
	}

	clientID := ""
	if v, ok := data["redirect_client_id"]; ok && !isFalsy(v) {
		clientID = strOf(v, "")
	}
	if clientID == "" {
		parts := strings.Split(msg.Topic(), "/")
		if len(parts) >= 2 {
			clientID = parts[1]
		}
	}
	if clientID == "" {
		clientID = "unknown-client"
	}

	native := map[string]any{
		"topic":       msg.Topic(),
		"client_id":   clientID,
		"text":        content,
		"raw_payload": payload,
	}
	c.Enqueue(native)
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// SendText publishes text to the subject's outbound topic (mirrors _send_text).
func (c *MQTTChannel) SendText(_ context.Context, subject *imgateway.ChannelSubject, text string) error {
	c.mu.Lock()
	client := c.client
	connected := c.connected
	c.mu.Unlock()
	if client == nil || !connected || strings.TrimSpace(text) == "" {
		return nil
	}
	clientID := ""
	if v, ok := subject.Metadata["client_id"]; ok && !isFalsy(v) {
		clientID = strOf(v, "")
	}
	if clientID == "" {
		clientID = subject.SubjectID
	}
	topic := strings.ReplaceAll(c.config.PublishTopic, "{client_id}", clientID)
	client.Publish(topic, byte(c.config.QoS), false, text)
	logf("DEBUG", "MQTT published to %s (%d chars)", topic, len(text))
	return nil
}

// SendContent sends content parts (mirrors _send_content).
func (c *MQTTChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText {
			if err := c.SendText(ctx, subject, part.Text); err != nil {
				return err
			}
		} else {
			if err := c.SendMedia(ctx, subject, part); err != nil {
				return err
			}
		}
	}
	return nil
}

// SendMedia sends a media part as a text marker (mirrors _send_media).
func (c *MQTTChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	label := c.MediaLabel(media)
	url := media.URL
	hasInline := media.Data != "" || media.LocalPath != ""
	if url != "" {
		return c.SendText(ctx, subject, fmt.Sprintf("[%s: %s]", label, url))
	} else if hasInline {
		return c.SendText(ctx, subject, fmt.Sprintf("[%s (local file, not deliverable on MQTT)]", label))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Inbound Parsing
// ---------------------------------------------------------------------------

// ParseInbound parses an MQTT native dict into InboundMessage.
func (c *MQTTChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok || data == nil {
		data = map[string]any{}
	}

	clientID := strOf(data["client_id"], "")
	if clientID == "" {
		clientID = "unknown-client"
	}
	text := strOf(data["text"], "")
	topic := strOf(data["topic"], "")

	rawPayloadValue := any("")
	if v, ok := data["raw_payload"]; ok {
		rawPayloadValue = v
	}
	metadata := map[string]any{
		"client_id":   clientID,
		"topic":       topic,
		"raw_payload": rawPayloadValue,
	}

	session := ""
	c.mu.Lock()
	session = c.connectionSession
	c.mu.Unlock()
	if session == "" {
		session = fmt.Sprintf("%s-unconnected", c.ChannelID())
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: clientID, ChatType: "direct", Metadata: metadata},
		ChannelSessionID: session,
		Content:          []imgateway.ContentPart{imgateway.NewTextPart(text)},
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}, nil
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// strOf converts a JSON-decoded value to string (mirrors Python str()).
func strOf(v any, def string) string {
	if v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// isFalsy mirrors Python truthiness for common JSON-decoded values.
func isFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}
