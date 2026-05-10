package core

import (
	"encoding/json"
	"fmt"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role
	Content []ContentPart
}

type ContentPart interface {
	contentPart()
	MarshalJSON() ([]byte, error)
}

type TextPart struct {
	Text string `json:"text"`
}

func (TextPart) contentPart() {}

func (p TextPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"type": "text", "text": p.Text})
}

type ReasoningPart struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func (ReasoningPart) contentPart() {}

func (p ReasoningPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "reasoning", "text": p.Text, "signature": p.Signature})
}

type ImagePart struct {
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func (ImagePart) contentPart() {}

func (p ImagePart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "image", "url": p.URL, "data": p.Data, "mime_type": p.MIMEType, "detail": p.Detail})
}

type AudioPart struct {
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

func (AudioPart) contentPart() {}

func (p AudioPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "audio", "url": p.URL, "data": p.Data, "mime_type": p.MIMEType})
}

type DocumentPart struct {
	Data     []byte `json:"data"`
	MIMEType string `json:"mime_type"`
	Name     string `json:"name,omitempty"`
}

func (DocumentPart) contentPart() {}

func (p DocumentPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "document", "data": p.Data, "mime_type": p.MIMEType, "name": p.Name})
}

type ToolCallPart struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (ToolCallPart) contentPart() {}

func (p ToolCallPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "tool_call", "id": p.ID, "name": p.Name, "arguments": p.Arguments})
}

type ToolResultPart struct {
	ToolCallID string        `json:"tool_call_id"`
	Content    []ContentPart `json:"content"`
	IsError    bool          `json:"is_error"`
}

func (ToolResultPart) contentPart() {}

func (p ToolResultPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "tool_result", "tool_call_id": p.ToolCallID, "content": p.Content, "is_error": p.IsError})
}

func unmarshalContentPart(raw []byte) (ContentPart, error) {
	var typ struct{ Type string `json:"type"` }
	if err := json.Unmarshal(raw, &typ); err != nil {
		return nil, err
	}
	switch typ.Type {
	case "text":
		var p TextPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "reasoning":
		var p ReasoningPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "image":
		var p ImagePart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "audio":
		var p AudioPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "document":
		var p DocumentPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "tool_call":
		var p ToolCallPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	case "tool_result":
		var p ToolResultPart
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("unknown content part type: %q", typ.Type)
	}
}

func (p *ToolResultPart) UnmarshalJSON(data []byte) error {
	aux := struct {
		ToolCallID string            `json:"tool_call_id"`
		Content    []json.RawMessage `json:"content"`
		IsError    bool              `json:"is_error"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	p.ToolCallID = aux.ToolCallID
	p.IsError = aux.IsError
	for _, raw := range aux.Content {
		part, err := unmarshalContentPart(raw)
		if err != nil {
			return err
		}
		p.Content = append(p.Content, part)
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"role":    m.Role,
		"content": m.Content,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	aux := struct {
		Role    Role              `json:"role"`
		Content []json.RawMessage `json:"content"`
	}{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Role = aux.Role
	for _, raw := range aux.Content {
		part, err := unmarshalContentPart(raw)
		if err != nil {
			return err
		}
		m.Content = append(m.Content, part)
	}
	return nil
}
