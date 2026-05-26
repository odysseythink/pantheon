package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

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
	SearchEndpoint   string
	ScrapeEndpoint   string
}

// NewWebBrowsing creates a web browsing plugin.
func NewWebBrowsing(cfg WebBrowsingConfig) conversation.Plugin {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.SearchEndpoint == "" {
		cfg.SearchEndpoint = "https://google.serper.dev/search"
	}
	if cfg.ScrapeEndpoint == "" {
		cfg.ScrapeEndpoint = "https://chrome.browserless.io/content"
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
	payload, err := json.Marshal(map[string]string{"q": query})
	if err != nil {
		return "", fmt.Errorf("marshal search payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.cfg.SearchEndpoint, bytes.NewReader(payload))
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read search response: %w", err)
	}
	return string(body), nil
}

// Scrape fetches and converts a webpage to markdown via browserless.io.
func (p *webBrowsingPlugin) Scrape(ctx context.Context, url string) (string, error) {
	if p.cfg.BrowserlessToken == "" {
		return "", fmt.Errorf("browserless token not configured")
	}
	payload, err := json.Marshal(map[string]string{"url": url})
	if err != nil {
		return "", fmt.Errorf("marshal scrape payload: %w", err)
	}
	endpoint := fmt.Sprintf("%s?token=%s", strings.TrimSuffix(p.cfg.ScrapeEndpoint, "/"), p.cfg.BrowserlessToken)
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
		return "", fmt.Errorf("scrape returned HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	html, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read scrape response: %w", err)
	}
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
