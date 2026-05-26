package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/odysseythink/pantheon/conversation"
	"github.com/odysseythink/pantheon/core"
)

// WebBrowsingConfig for the web browsing plugin.
type WebBrowsingConfig struct {
	SerperAPIKey     string
	BrowserlessToken string
	SummarizerModel  core.LanguageModel
	HTTPClient       *http.Client
}

// NewWebBrowsing creates a web browsing plugin.
func NewWebBrowsing(cfg WebBrowsingConfig) conversation.Plugin {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &webBrowsingPlugin{cfg: cfg}
}

type webBrowsingPlugin struct {
	cfg WebBrowsingConfig
}

func (p *webBrowsingPlugin) Name() string { return "web-browsing" }

func (p *webBrowsingPlugin) Setup(conv *conversation.Conversation) error {
	// This plugin registers tools via the participant's agent, not on the conversation directly.
	// Users should create a tool.Registry, register search/scrape tools, and pass to agent.New().
	return nil
}

// Search performs a Google search via serper.dev.
func (p *webBrowsingPlugin) Search(ctx context.Context, query string) (string, error) {
	if p.cfg.SerperAPIKey == "" {
		return "", fmt.Errorf("serper API key not configured")
	}
	payload, _ := json.Marshal(map[string]string{"q": query})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://google.serper.dev/search", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-API-KEY", p.cfg.SerperAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// Scrape fetches and converts a webpage to markdown via browserless.io.
func (p *webBrowsingPlugin) Scrape(ctx context.Context, url string) (string, error) {
	if p.cfg.BrowserlessToken == "" {
		return "", fmt.Errorf("browserless token not configured")
	}
	payload, _ := json.Marshal(map[string]string{"url": url})
	endpoint := fmt.Sprintf("https://chrome.browserless.io/content?token=%s", p.cfg.BrowserlessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status), nil
	}

	html, _ := io.ReadAll(resp.Body)
	converter := htmltomarkdown.NewConverter("", true, nil)
	md, err := converter.ConvertString(string(html))
	if err != nil {
		return string(html), nil // fallback to raw html
	}

	if len(md) <= 8000 || p.cfg.SummarizerModel == nil {
		return md, nil
	}
	return p.summarize(ctx, md)
}

func (p *webBrowsingPlugin) summarize(ctx context.Context, text string) (string, error) {
	req := &core.Request{
		Messages: []core.Message{
			core.NewTextMessage(core.MESSAGE_ROLE_USER, fmt.Sprintf("Summarize the following text concisely:\n\n%s", text)),
		},
	}
	resp, err := p.cfg.SummarizerModel.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Message.Text(), nil
}
