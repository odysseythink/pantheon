// Package native provides a local embedding and reranker provider using the
// Cybertron library (github.com/nlpodyssey/cybertron), which runs BERT-based
// models in pure Go without CGO.
//
// Embedding models supported:
//   - sentence-transformers/all-MiniLM-L6-v2
//   - sentence-transformers/LaBSE
//   - Xenova/all-MiniLM-L6-v2
//   - nomic-ai/nomic-embed-text-v1
//   - intfloat/multilingual-e5-small
//
// Reranker models are loaded as BERT text-classification (cross-encoder)
// models and score query-document pairs for relevance.
//
// Models must be downloaded and converted to the Cybertron format beforehand.
// Use the cybertron CLI or the huggingface-go tools to prepare models.
package native
