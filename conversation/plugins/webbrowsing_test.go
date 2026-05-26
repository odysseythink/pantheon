package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/conversation"
	"github.com/stretchr/testify/require"
)

func TestWebBrowsingPlugin_Name(t *testing.T) {
	p := NewWebBrowsing(WebBrowsingConfig{})
	require.Equal(t, "web-browsing", p.Name())
}

func TestWebBrowsingPlugin_Setup(t *testing.T) {
	p := NewWebBrowsing(WebBrowsingConfig{})
	c := conversation.New()
	err := p.Setup(c)
	require.NoError(t, err)
}

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

// errorRoundTripper always returns a network error.
type errorRoundTripper struct {
	err error
}

func (e *errorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, e.err
}

func TestWebBrowsingPlugin_Search_HTTPError(t *testing.T) {
	client := &http.Client{Transport: &errorRoundTripper{err: errors.New("network down")}}
	plugin := NewWebBrowsing(WebBrowsingConfig{
		SerperAPIKey: "key",
		HTTPClient:   client,
	})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Search(context.Background(), "query")
	require.Error(t, err)
	require.Contains(t, err.Error(), "network down")
}

// errorReadCloser returns an error on Read.
type errorReadCloser struct{}

func (e *errorReadCloser) Read(p []byte) (int, error) { return 0, errors.New("read body error") }
func (e *errorReadCloser) Close() error                { return nil }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWebBrowsingPlugin_Search_ReadBodyError(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       &errorReadCloser{},
			Header:     make(http.Header),
		}, nil
	})}
	plugin := NewWebBrowsing(WebBrowsingConfig{
		SerperAPIKey: "key",
		HTTPClient:   client,
	})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Search(context.Background(), "query")
	require.Error(t, err)
	require.Contains(t, err.Error(), "read body error")
}

func TestWebBrowsingPlugin_Scrape_Non200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer server.Close()

	plugin := NewWebBrowsing(WebBrowsingConfig{
		BrowserlessToken: "test-token",
		ScrapeEndpoint:   server.URL,
	})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Scrape(context.Background(), "https://example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "404")
}

func TestWebBrowsingPlugin_Scrape_ReadBodyError(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       &errorReadCloser{},
			Header:     make(http.Header),
		}, nil
	})}
	plugin := NewWebBrowsing(WebBrowsingConfig{
		BrowserlessToken: "token",
		HTTPClient:       client,
	})
	p := plugin.(*webBrowsingPlugin)
	_, err := p.Scrape(context.Background(), "https://example.com")
	require.Error(t, err)
	require.Contains(t, err.Error(), "read body error")
}

func TestWebBrowsingPlugin_Scrape_LongTextSummarize(t *testing.T) {
	// Build a long HTML (>8000 chars after markdown conversion).
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for i := 0; i < 500; i++ {
		sb.WriteString("<p>This is a paragraph with some content that will be converted to markdown and exceed the 8000 character threshold for summarization.</p>")
	}
	sb.WriteString("</body></html>")
	longHTML := sb.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(longHTML))
	}))
	defer server.Close()

	plugin := NewWebBrowsing(WebBrowsingConfig{
		BrowserlessToken: "test-token",
		ScrapeEndpoint:   server.URL,
		SummarizerModel:  newRealModel(t),
	})
	p := plugin.(*webBrowsingPlugin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := p.Scrape(ctx, "https://example.com")
	require.NoError(t, err)
	require.NotEmpty(t, result)
}

func TestWebBrowsingPlugin_Summarize_RealModel(t *testing.T) {
	model := newRealModel(t)
	p := NewWebBrowsing(WebBrowsingConfig{
		SummarizerModel: model,
	}).(*webBrowsingPlugin)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	longText := strings.Repeat("This is a long text that needs summarization. ", 200)
	result, err := p.summarize(ctx, longText)
	require.NoError(t, err)
	require.NotEmpty(t, result)
}
