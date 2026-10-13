// Package localonnx runs local ONNX embedding models for the knowledge base
// (fastembed-compatible catalogs and cache layouts, no Python involved).
//
// Inference uses github.com/yalue/onnxruntime_go, which loads the ONNX
// Runtime shared library (onnxruntime.dll / libonnxruntime.so) at runtime —
// the Go build stays CGO-free and the library is treated the same way Python
// treats the fastembed/onnxruntime pip packages: when it is missing the
// service reports deps unavailable (503) and the download flow can fetch it.
package localonnx

import (
	"os"
	"path/filepath"
	"strings"
)

// TokenizerKind enumerates the tokenizer families this package can run.
type TokenizerKind string

const (
	// TokenizerWordPiece covers the BERT family (BertNormalizer +
	// BertPreTokenizer + WordPiece + [CLS]/[SEP] templates): every bge,
	// MiniLM, jina-embeddings-v2 and gte entry of the catalog.
	TokenizerWordPiece TokenizerKind = "wordpiece"
	// TokenizerUnigram covers XLM-R / sentencepiece models
	// (multilingual-e5-large, paraphrase-multilingual-MiniLM). Not
	// implemented in the Go build; entries are reported as supported=false
	// and download/enable requests fail with an explicit message.
	TokenizerUnigram TokenizerKind = "unigram"
)

// PoolingKind mirrors the fastembed post-processing split.
type PoolingKind string

const (
	// PoolingCLS takes the [CLS] vector when the model emits a
	// (batch, seq, dim) tensor (the fastembed OnnxTextEmbedding behavior);
	// 2d outputs pass through. Both are L2-normalized.
	PoolingCLS PoolingKind = "cls"
	// PoolingMean performs attention-mask mean pooling then L2-normalizes
	// (the fastembed PooledNormalizedEmbedding behavior).
	PoolingMean PoolingKind = "mean"
)

// Model is one catalog entry (ports onnx_catalog.py's fallback tables plus
// the fastembed 0.8 DenseModelDescription metadata for the same ids).
type Model struct {
	ID          string
	Dim         int
	SizeGB      float64
	HFSource    string // Hugging Face repo that mirrors the weights
	ModelFile   string // primary ONNX weights file inside the snapshot
	Pooling     PoolingKind
	Tokenizer   TokenizerKind
	Recommended bool
}

// PresetModelIDs ports ONNX_PRESET_MODEL_IDS (recommended order).
var PresetModelIDs = []string{
	"BAAI/bge-small-zh-v1.5",
	"jinaai/jina-embeddings-v2-base-zh",
	"intfloat/multilingual-e5-large",
}

var catalog = []Model{
	// Presets first (the extra catalog preserves the Python ordering:
	// presets, then the sorted extras).
	{ID: "BAAI/bge-small-zh-v1.5", Dim: 512, SizeGB: 0.09, HFSource: "Qdrant/bge-small-zh-v1.5",
		ModelFile: "model_optimized.onnx", Pooling: PoolingCLS, Tokenizer: TokenizerWordPiece, Recommended: true},
	{ID: "jinaai/jina-embeddings-v2-base-zh", Dim: 768, SizeGB: 0.32, HFSource: "jinaai/jina-embeddings-v2-base-zh",
		ModelFile: "onnx/model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece, Recommended: true},
	{ID: "intfloat/multilingual-e5-large", Dim: 1024, SizeGB: 1.2, HFSource: "qdrant/multilingual-e5-large-onnx",
		ModelFile: "model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerUnigram, Recommended: true},

	{ID: "BAAI/bge-small-en-v1.5", Dim: 384, SizeGB: 0.13, HFSource: "Qdrant/bge-small-en-v1.5-onnx-Q",
		ModelFile: "model_optimized.onnx", Pooling: PoolingCLS, Tokenizer: TokenizerWordPiece},
	{ID: "BAAI/bge-base-en-v1.5", Dim: 768, SizeGB: 0.21, HFSource: "Qdrant/bge-base-en-v1.5-onnx-Q",
		ModelFile: "model_optimized.onnx", Pooling: PoolingCLS, Tokenizer: TokenizerWordPiece},
	{ID: "BAAI/bge-large-en-v1.5", Dim: 1024, SizeGB: 0.53, HFSource: "qdrant/bge-large-en-v1.5-onnx",
		ModelFile: "model.onnx", Pooling: PoolingCLS, Tokenizer: TokenizerWordPiece},
	{ID: "jinaai/jina-embeddings-v2-base-en", Dim: 768, SizeGB: 0.32, HFSource: "xenova/jina-embeddings-v2-base-en",
		ModelFile: "onnx/model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece},
	{ID: "jinaai/jina-embeddings-v2-small-en", Dim: 512, SizeGB: 0.13, HFSource: "xenova/jina-embeddings-v2-small-en",
		ModelFile: "onnx/model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece},
	{ID: "sentence-transformers/all-MiniLM-L6-v2", Dim: 384, SizeGB: 0.09, HFSource: "qdrant/all-MiniLM-L6-v2-onnx",
		ModelFile: "model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece},
	{ID: "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2", Dim: 384, SizeGB: 0.12,
		HFSource:  "qdrant/paraphrase-multilingual-MiniLM-L12-v2-onnx-Q",
		ModelFile: "model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerUnigram},
	{ID: "thenlper/gte-base", Dim: 768, SizeGB: 0.22, HFSource: "thenlper/gte-base",
		ModelFile: "onnx/model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece},
	{ID: "thenlper/gte-large", Dim: 1024, SizeGB: 0.67, HFSource: "qdrant/gte-large-onnx",
		ModelFile: "model.onnx", Pooling: PoolingMean, Tokenizer: TokenizerWordPiece},
}

// Catalog returns the full model list in the Python fallback ordering
// (presets first, then the sorted extras).
func Catalog() []Model {
	out := make([]Model, len(catalog))
	copy(out, catalog)
	return out
}

// Find resolves a model id; ok=false when the id is not in the catalog.
func Find(id string) (Model, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// ModelIDRE-lite validation ports normalize_onnx_model_id's guard rails.
// Catalog membership is enforced separately; this only rejects unsafe ids.
func ValidateModelID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	if strings.Contains(id, "..") || strings.Contains(id, "\\") ||
		strings.HasPrefix(id, "/") || strings.HasSuffix(id, "/") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/':
		default:
			return false
		}
	}
	return true
}

// CacheDir returns <root>/embedding_models (ports embedding_models_dir()).
func CacheDir(root string) string {
	return filepath.Join(root, "embedding_models")
}

// RuntimeDir returns the directory holding the ONNX Runtime shared library.
// Python installs it via pip; the Go build keeps it under <root>/onnxruntime.
// Override with the ODYBOX_ONNX_RUNTIME_DIR environment variable.
func RuntimeDir(root string) string {
	if v := strings.TrimSpace(os.Getenv("ODYBOX_ONNX_RUNTIME_DIR")); v != "" {
		return v
	}
	return filepath.Join(root, "onnxruntime")
}

// snapshotDirs lists the cache directories that may hold *model*'s weights,
// ports _alias_cache_names: the public id, the HF mirror repo, and the legacy
// flat / fast- prefixed names.
func snapshotDirs(root string, m Model) []string {
	base := CacheDir(root)
	id := m.ID
	if !ValidateModelID(id) {
		return nil
	}
	dirs := []string{
		filepath.Join(base, "models--"+strings.ReplaceAll(id, "/", "--")),
	}
	if m.HFSource != "" {
		dirs = append(dirs, filepath.Join(base,
			"models--"+strings.ReplaceAll(m.HFSource, "/", "--")))
	}
	suffix := id
	if i := strings.LastIndex(id, "/"); i >= 0 {
		suffix = id[i+1:]
	}
	return append(dirs,
		filepath.Join(base, suffix),
		filepath.Join(base, "fast-"+suffix),
	)
}

// HasONNXWeights reports whether dir holds at least one complete ONNX weight
// file at any depth (ports _cache_has_onnx_weights, which rglobs the cache
// dir to cover both the flat layout and the HF snapshots/<rev>/ layout).
func HasONNXWeights(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			if found {
				return filepath.SkipAll
			}
			return nil //nolint:nilerr // continue walking
		}
		if strings.HasSuffix(strings.ToLower(path), ".onnx") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// IsModelDownloaded ports is_model_downloaded for one catalog entry.
func IsModelDownloaded(root string, m Model) bool {
	for _, dir := range snapshotDirs(root, m) {
		if HasONNXWeights(dir) {
			return true
		}
	}
	// The .downloaded marker is honored too (written by the Python build).
	marker := filepath.Join(CacheDir(root), ".downloaded",
		strings.ReplaceAll(m.ID, "/", "--"))
	if _, err := os.Stat(marker); err == nil {
		return true
	}
	return false
}

// ListDownloaded returns the catalog ids whose weights are on disk.
func ListDownloaded(root string) []string {
	var out []string
	for _, m := range catalog {
		if IsModelDownloaded(root, m) {
			out = append(out, m.ID)
		}
	}
	return out
}

// DeleteModel removes every cache location for a model (ports
// delete_downloaded_model) and drops any cached session.
func DeleteModel(root, modelID string) bool {
	m, ok := Find(modelID)
	removed := false
	drop := func(dir string) {
		if _, err := os.Stat(dir); err == nil {
			if err := os.RemoveAll(dir); err == nil {
				removed = true
			}
		}
	}
	if ok {
		for _, dir := range snapshotDirs(root, m) {
			drop(dir)
		}
	}
	drop(filepath.Join(CacheDir(root), ".downloaded",
		strings.ReplaceAll(modelID, "/", "--")))
	CloseSession(modelID)
	return removed
}
