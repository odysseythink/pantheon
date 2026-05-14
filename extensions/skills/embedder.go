package skills

import "context"

// Embedder is the single-text vectorizer the Retriever uses.
// Pantheon's extensions/embed package and hermind's tool/embedding
// package both satisfy this interface.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}
