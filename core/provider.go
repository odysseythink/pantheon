package core

import "context"

// Provider is a factory for language models.
type Provider interface {
	Name() string
	LanguageModel(ctx context.Context, modelID string) (LanguageModel, error)
}

// LanguageModel is the unified interface for all LLM backends.
type LanguageModel interface {
	Generate(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (StreamResponse, error)
	GenerateObject(ctx context.Context, req *ObjectRequest) (*ObjectResponse, error)
	StreamObject(ctx context.Context, req *ObjectRequest) (ObjectStreamResponse, error)
	Provider() string
	Model() string
}
