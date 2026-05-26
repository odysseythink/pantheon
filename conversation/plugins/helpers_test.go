package plugins

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

// realModel wraps openaicompat.Client to implement core.LanguageModel for integration tests.
type realModel struct {
	client *openaicompat.Client
	model  string
}

func (m *realModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.ChatCompletion(ctx, m.model, req)
}

func (m *realModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (m *realModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (m *realModel) Provider() string { return "integration" }
func (m *realModel) Model() string    { return m.model }

func newRealModel(t *testing.T) core.LanguageModel {
	t.Helper()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
	client := openaicompat.NewClient(
		"http://192.168.11.150:8989",
		apiKey,
	)
	client.HTTPClient = &http.Client{Timeout: 120 * time.Second}
	return &realModel{client: client, model: "kb-big"}
}

// mockModel implements core.LanguageModel for controlled test responses.
type mockModel struct {
	responses []string
	index     int
	err       error
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.index >= len(m.responses) {
		return &core.Response{
			Message: core.Message{Content: core.NewTextContent("TERMINATE")},
		}, nil
	}
	resp := m.responses[m.index]
	m.index++
	return &core.Response{
		Message: core.Message{Content: core.NewTextContent(resp)},
	}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock-model" }
