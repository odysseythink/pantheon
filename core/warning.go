package core

// CallWarningType classifies the kind of warning emitted by a provider.
type CallWarningType string

const (
	// CallWarningTypeUnsupportedSetting indicates a setting was ignored because
	// the provider or model does not support it.
	CallWarningTypeUnsupportedSetting CallWarningType = "unsupported-setting"
	// CallWarningTypeUnsupportedTool indicates a tool was dropped because the
	// provider does not support it.
	CallWarningTypeUnsupportedTool CallWarningType = "unsupported-tool"
	// CallWarningTypeOther is a catch-all for other non-fatal issues.
	CallWarningTypeOther CallWarningType = "other"
)

// CallWarning represents a non-fatal issue reported by a provider.
// The call proceeds but some settings or tools may have been ignored.
type CallWarning struct {
	Type    CallWarningType `json:"type"`
	Setting string          `json:"setting,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Message string          `json:"message,omitempty"`
}
