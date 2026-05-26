package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebBrowsingPlugin_Search(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "test-key", r.Header.Get("X-API-KEY"))
		w.WriteHeader(200)
		w.Write([]byte(`{"answerBox": {"snippet": "Result"}}`))
	}))
	defer server.Close()

	plugin := NewWebBrowsing(WebBrowsingConfig{
		SerperAPIKey:   "test-key",
		SearchEndpoint: server.URL,
	})
	p := plugin.(*webBrowsingPlugin)

	result, err := p.Search(context.Background(), "test query")
	require.NoError(t, err)
	require.Contains(t, result, "Result")
}

func TestWebBrowsingPlugin_Scrape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		w.WriteHeader(200)
		w.Write([]byte("<html><body><h1>Hello</h1></body></html>"))
	}))
	defer server.Close()

	plugin := NewWebBrowsing(WebBrowsingConfig{
		BrowserlessToken: "test-token",
		ScrapeEndpoint:   server.URL,
	})
	p := plugin.(*webBrowsingPlugin)

	result, err := p.Scrape(context.Background(), "https://example.com")
	require.NoError(t, err)
	require.True(t, strings.Contains(result, "Hello") || strings.Contains(result, "# Hello"), "result should contain Hello: %s", result)
}

func TestWebBrowsingPlugin_Search_MissingKey(t *testing.T) {
	plugin := NewWebBrowsing(WebBrowsingConfig{})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Search(context.Background(), "query")
	require.Error(t, err)
	require.Contains(t, err.Error(), "serper API key")
}

func TestWebBrowsingPlugin_Scrape_MissingToken(t *testing.T) {
	plugin := NewWebBrowsing(WebBrowsingConfig{})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Scrape(context.Background(), "https://example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "browserless token")
}
