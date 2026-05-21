package rerank

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Provider extends core.Provider with rerank capabilities.
type Provider interface {
	core.Provider
	RerankModel(ctx context.Context, modelID string) (RerankModel, error)
}

// RerankModel performs relevance-based reranking of documents against a query.
type RerankModel interface {
	Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error)
}

// RerankRequest holds parameters for a rerank operation.
// Fields align with Cohere Rerank v2 API for maximum compatibility.
type RerankRequest struct {
	Query           string
	Documents       []string
	TopN            int
	ReturnDocuments bool
	MaxChunksPerDoc int
	ProviderOptions core.ProviderOptions
}

// RerankResponse holds reranked results and token usage.
type RerankResponse struct {
	ID      string
	Results []RerankResult
	Usage   core.Usage
}

// RerankResult is a single reranked document with its relevance score.
type RerankResult struct {
	Index          int
	RelevanceScore float32
	Document       string
}
