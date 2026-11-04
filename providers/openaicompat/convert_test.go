package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToOpenAITools_WithProviderOptions(t *testing.T) {
	// OpenAI does not have standard per-tool options, but the function should not panic.
	tools := []core.ToolDefinition{{
		Name:            "test",
		Description:     "test tool",
		Parameters:      nil,
		ProviderOptions: core.ProviderOptions{},
	}}
	out := ToOpenAITools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
}

func ptr(s string) *string { return &s }

func TestContentToOpenAI_ImageInlineData(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake-png-payload")
	parts := []core.ContentParter{
		core.TextPart{Text: "what is this?"},
		core.ImagePart{Data: pngBytes, MIMEType: "image/png", Detail: "high"},
	}

	out, err := contentToOpenAI(parts)
	if err != nil {
		t.Fatalf("contentToOpenAI() error = %v", err)
	}
	content, ok := out.([]ContentParter)
	if !ok {
		t.Fatalf("contentToOpenAI() = %T, want []ContentParter", out)
	}
	if len(content) != 2 {
		t.Fatalf("got %d parts, want 2", len(content))
	}
	img := content[1]
	if img.Type != "image_url" || img.ImageURL == nil {
		t.Fatalf("part[1] = %+v, want image_url part", img)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(img.ImageURL.URL, prefix) {
		t.Fatalf("image url = %q, want data uri prefix %q", img.ImageURL.URL, prefix)
	}
	if img.ImageURL.Detail != "high" {
		t.Errorf("detail = %q, want %q", img.ImageURL.Detail, "high")
	}
}

func TestContentToOpenAI_ImageWithoutSource(t *testing.T) {
	parts := []core.ContentParter{core.ImagePart{MIMEType: "image/png"}}
	if _, err := contentToOpenAI(parts); err == nil {
		t.Fatal("contentToOpenAI() error = nil, want error for image without url or data")
	}
}

// TestToOpenAIMessages_InlineImageWireFormat locks the wire contract: an image
// part carrying only raw bytes must reach the request body as a data URI, never
// as an empty url (which OpenAI-compatible endpoints reject with
// "image url is empty").
func TestToOpenAIMessages_InlineImageWireFormat(t *testing.T) {
	msgs := []core.Message{{
		Role: core.MESSAGE_ROLE_USER,
		Content: []core.ContentParter{
			core.TextPart{Text: "see picture"},
			core.ImagePart{Data: []byte("\x89PNG\r\n\x1a\nfake-png-payload"), MIMEType: "image/png"},
		},
	}}

	out, err := ToOpenAIMessages(msgs, "")
	if err != nil {
		t.Fatalf("ToOpenAIMessages() error = %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	body := string(raw)
	if strings.Contains(body, `"url":""`) {
		t.Fatalf("request body still carries an empty image url: %s", body)
	}
	if !strings.Contains(body, `"url":"data:image/png;base64,`) {
		t.Fatalf("request body has no inline data uri: %s", body)
	}
}
