// Configuration models for the Tencent Yuanbao channel (ports config.py).
package yuanbao

import (
	"runtime"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// YuanbaoToken mirrors _YuanbaoToken.
type YuanbaoToken struct {
	Token     string
	BotID     string
	Source    string
	ExpiresAt float64
	Duration  int
}

// YuanbaoConfig mirrors the YuanbaoConfig dataclass.
//
// The current Yuanbao bot binding flow returns app_key / app_secret. The
// gateway uses them to call /api/v5/robotLogic/sign-token and then binds the
// returned token over the configured WebSocket URL.
type YuanbaoConfig struct {
	imgateway.ChannelConfig

	AppKey    string `json:"app_key,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
	APIDomain string `json:"api_domain,omitempty"`
	WSURL     string `json:"ws_url,omitempty"`
	RouteEnv  string `json:"route_env,omitempty"`

	// Legacy/compat fields. They are retained so old stored configs can
	// still be loaded, but live Yuanbao connections use app_key/app_secret.
	APIBase    string         `json:"api_base,omitempty"`
	Token      string         `json:"token,omitempty"`
	BotID      string         `json:"bot_id,omitempty"`
	Identifier string         `json:"identifier,omitempty"`
	Source     string         `json:"source,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`

	AppVersion         string  `json:"app_version,omitempty"`
	AppOperationSystem string  `json:"app_operation_system,omitempty"`
	OperationSystem    string  `json:"operation_system,omitempty"`
	BotVersion         string  `json:"bot_version,omitempty"`
	InstanceID         string  `json:"instance_id,omitempty"`
	RequestTimeout     float64 `json:"request_timeout,omitempty"`
	HeartbeatInterval  float64 `json:"heartbeat_interval,omitempty"`
	ConnectTimeout     float64 `json:"connect_timeout,omitempty"`
	ProbeMode          string  `json:"probe_mode,omitempty"`
}

// fieldAliases mirrors YuanbaoConfig.field_aliases.
var fieldAliases = map[string]string{
	"appId":              "app_key",
	"appKey":             "app_key",
	"client_id":          "app_key",
	"appSecret":          "app_secret",
	"client_secret":      "app_secret",
	"api_base":           "api_domain",
	"apiBase":            "api_domain",
	"apiDomain":          "api_domain",
	"websocket_url":      "ws_url",
	"webSocketURL":       "ws_url",
	"wsUrl":              "ws_url",
	"routeEnv":           "route_env",
	"probeMode":          "probe_mode",
	"botId":              "bot_id",
	"identifier":         "bot_id",
	"operationSystem":    "app_operation_system",
	"appOperationSystem": "app_operation_system",
}

// NewYuanbaoConfig returns a config with the dataclass defaults.
func NewYuanbaoConfig() *YuanbaoConfig {
	return &YuanbaoConfig{
		AppVersion:        "1.0.0",
		BotVersion:        "1.0.0",
		InstanceID:        "17", // str(HERMES_INSTANCE_ID)
		RequestTimeout:    30.0,
		HeartbeatInterval: 30.0,
		ConnectTimeout:    15.0,
		ProbeMode:         "full",
	}
}

// FromDict builds the config from a plain dict (mirrors from_dict + the
// dataclass defaults, normalization happens in Normalize).
func (c *YuanbaoConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, fieldAliases)
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.AppKey = imgateway.Str(data, "app_key", c.AppKey)
	c.AppSecret = imgateway.Str(data, "app_secret", c.AppSecret)
	c.APIDomain = imgateway.Str(data, "api_domain", c.APIDomain)
	c.WSURL = imgateway.Str(data, "ws_url", c.WSURL)
	c.RouteEnv = imgateway.Str(data, "route_env", c.RouteEnv)
	c.APIBase = imgateway.Str(data, "api_base", c.APIBase)
	c.Token = imgateway.Str(data, "token", c.Token)
	c.BotID = imgateway.Str(data, "bot_id", c.BotID)
	c.Identifier = imgateway.Str(data, "identifier", c.Identifier)
	c.Source = imgateway.Str(data, "source", c.Source)
	if extra, ok := imgateway.Map(data, "extra"); ok {
		c.Extra = extra
	}
	c.AppVersion = imgateway.Str(data, "app_version", c.AppVersion)
	c.AppOperationSystem = imgateway.Str(data, "app_operation_system", c.AppOperationSystem)
	c.OperationSystem = imgateway.Str(data, "operation_system", c.OperationSystem)
	c.BotVersion = imgateway.Str(data, "bot_version", c.BotVersion)
	c.InstanceID = imgateway.Str(data, "instance_id", c.InstanceID)
	c.RequestTimeout = imgateway.Float(data, "request_timeout", c.RequestTimeout)
	c.HeartbeatInterval = imgateway.Float(data, "heartbeat_interval", c.HeartbeatInterval)
	c.ConnectTimeout = imgateway.Float(data, "connect_timeout", c.ConnectTimeout)
	c.ProbeMode = imgateway.Str(data, "probe_mode", c.ProbeMode)
	return nil
}

// Normalize mirrors __post_init__.
func (c *YuanbaoConfig) Normalize() {
	domain := c.APIDomain
	if domain == "" {
		domain = c.APIBase
	}
	c.APIDomain = NormalizeHTTPOrigin(orDefault(domain, DefaultAPIDomain))
	c.APIBase = c.APIDomain
	c.WSURL = NormalizeWSURL(orDefault(c.WSURL, DefaultWSURL))
	selectedOperationSystem := c.AppOperationSystem
	if selectedOperationSystem == "" {
		selectedOperationSystem = c.OperationSystem
	}
	if selectedOperationSystem == "" {
		selectedOperationSystem = sysPlatform()
	}
	c.AppOperationSystem = selectedOperationSystem
	c.OperationSystem = selectedOperationSystem
	c.InstanceID = orDefault(c.InstanceID, "17") // str(self.instance_id or HERMES_INSTANCE_ID)
	if c.BotID == "" && c.Identifier != "" {
		c.BotID = c.Identifier
	}
	if c.Identifier == "" && c.BotID != "" {
		c.Identifier = c.BotID
	}
}

// MissingCredentials returns the required credential fields that are empty.
func (c *YuanbaoConfig) MissingCredentials() []string {
	var missing []string
	if c.AppKey == "" {
		missing = append(missing, "app_key")
	}
	if c.AppSecret == "" {
		missing = append(missing, "app_secret")
	}
	return missing
}

// sysPlatform approximates Python sys.platform for the running OS.
func sysPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	case "linux":
		return "linux"
	default:
		return runtime.GOOS
	}
}

func orDefault(value, def string) string {
	if value != "" {
		return value
	}
	return def
}
