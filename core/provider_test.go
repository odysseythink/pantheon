package core

import (
	"context"
	"testing"
)

// Compile-time interface checks.
func TestProviderInterface(t *testing.T) {
	// These should compile if the types implement the interfaces.
	var _ Provider = (*mockProvider)(nil)
	var _ LanguageModel = (*mockLanguageModel)(nil)
}

type mockProvider struct{}

func (m *mockProvider) Name() string { return "mock" }
func (m *mockProvider) Models(ctx context.Context) ([]Model, error) {
	return nil, nil
}
func (m *mockProvider) LanguageModel(ctx context.Context, modelID string) (LanguageModel, error) {
	return &mockLanguageModel{}, nil
}

type mockLanguageModel struct{}

func (m *mockLanguageModel) Generate(ctx context.Context, req *Request) (*Response, error) {
	return nil, nil
}
func (m *mockLanguageModel) Stream(ctx context.Context, req *Request) (StreamResponse, error) {
	return nil, nil
}
func (m *mockLanguageModel) GenerateObject(ctx context.Context, req *ObjectRequest) (*ObjectResponse, error) {
	return nil, nil
}
func (m *mockLanguageModel) Provider() string { return "mock" }
func (m *mockLanguageModel) Model() string    { return "mock-model" }

func TestMockProvider_Name(t *testing.T) {
	p := &mockProvider{}
	if p.Name() != "mock" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestMockLanguageModel_ProviderAndModel(t *testing.T) {
	m := &mockLanguageModel{}
	if m.Provider() != "mock" {
		t.Errorf("unexpected provider: %s", m.Provider())
	}
	if m.Model() != "mock-model" {
		t.Errorf("unexpected model: %s", m.Model())
	}
}
