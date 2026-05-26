package native

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nlpodyssey/cybertron/pkg/models/bert"
	"github.com/nlpodyssey/cybertron/pkg/tasks"
	bert_textclassification "github.com/nlpodyssey/cybertron/pkg/tasks/textclassification/bert"
	"github.com/nlpodyssey/cybertron/pkg/tokenizers"
	"github.com/nlpodyssey/cybertron/pkg/tokenizers/wordpiecetokenizer"
	"github.com/nlpodyssey/spago/mat"
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/rerank"
)

// RerankModel implements rerank.RerankModel for the native provider using a
// local BERT-based cross-encoder.
type RerankModel struct {
	provider    *Provider
	modelID     string
	doLowerCase bool
	once        sync.Once
	tc          *bert_textclassification.TextClassification
	loadErr     error
}

func (m *RerankModel) loadModel() error {
	m.once.Do(func() {
		modelDir := m.provider.modelDir
		modelName := m.modelID
		if modelName == "" {
			modelName = m.provider.modelName
		}
		conf := &tasks.Config{
			ModelsDir:           modelDir,
			ModelName:           modelName,
			DownloadPolicy:      tasks.DownloadNever,
			ConversionPolicy:    tasks.ConvertNever,
			ConversionPrecision: tasks.F32,
		}
		modelPath := conf.FullModelPath()

		// LoadTextClassification reads tokenizer_config.json internally, but
		// doLowerCase is unexported on TextClassification, so we read it here
		// to avoid duplicating the lowercasing logic from TextClassification.tokenize.
		tokenizerConfig, err := bert.ConfigFromFile[bert.TokenizerConfig](filepath.Join(modelPath, "tokenizer_config.json"))
		if err == nil {
			m.doLowerCase = tokenizerConfig.DoLowerCase
		}
		m.tc, m.loadErr = bert_textclassification.LoadTextClassification(modelPath)
	})
	if m.loadErr != nil {
		return fmt.Errorf("native rerank: failed to load model: %w", m.loadErr)
	}
	return nil
}

// tokenizePair tokenizes query and doc, then builds the
// [CLS] query [SEP] doc [SEP] sequence.
// It returns the full token sequence and the query length
// (number of tokens up to and including the first [SEP]).
func (m *RerankModel) tokenizePair(query, doc string) ([]string, int) {
	if m.doLowerCase {
		query = strings.ToLower(query)
		doc = strings.ToLower(doc)
	}
	queryTokens := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(query))
	docTokens := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(doc))
	return buildTokenPair(queryTokens, docTokens)
}

// buildTokenPair constructs the [CLS] query [SEP] doc [SEP] token sequence.
// It returns the tokens and the query length (including [CLS] and [SEP]).
func buildTokenPair(queryTokens, docTokens []string) ([]string, int) {
	tokens := make([]string, 0, 2+len(queryTokens)+len(docTokens))
	tokens = append(tokens, wordpiecetokenizer.DefaultClassToken)
	tokens = append(tokens, queryTokens...)
	tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
	tokens = append(tokens, docTokens...)
	tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
	queryLen := len(queryTokens) + 2 // [CLS] + queryTokens + [SEP]
	return tokens, queryLen
}

// truncate limits the token sequence to maxLen, preserving the query and
// truncating the document from the end if necessary.
func truncate(tokens []string, queryLen, maxLen int) []string {
	if len(tokens) <= maxLen {
		return tokens
	}
	sep := wordpiecetokenizer.DefaultSequenceSeparator
	if queryLen >= maxLen {
		return append(tokens[:maxLen-1], sep)
	}
	docMaxLen := maxLen - queryLen - 1
	if docMaxLen <= 0 {
		return tokens[:queryLen]
	}
	end := queryLen + docMaxLen
	result := make([]string, 0, maxLen)
	result = append(result, tokens[:end]...)
	result = append(result, sep)
	return result
}

func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x))
}

// Rerank scores each document against the query and returns them sorted by
// descending relevance. Model loading is lazy and errors are cached for the
// lifetime of the RerankModel (sync.Once semantics).
func (m *RerankModel) Rerank(ctx context.Context, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("native rerank: query is required")
	}
	if len(req.Documents) == 0 {
		return nil, fmt.Errorf("native rerank: documents cannot be empty")
	}
	if err := m.loadModel(); err != nil {
		return nil, err
	}

	maxLen := m.tc.Model.Bert.Config.MaxPositionEmbeddings
	results := make([]rerank.RerankResult, 0, len(req.Documents))
	for i, doc := range req.Documents {
		tokens, queryLen := m.tokenizePair(req.Query, doc)
		tokens = truncate(tokens, queryLen, maxLen)
		logitTensor := m.tc.Model.Classify(tokens)
		logit := mat.Data[float64](logitTensor)[0]
		score := sigmoid(logit)
		result := rerank.RerankResult{
			Index:          i,
			RelevanceScore: float32(score),
		}
		if req.ReturnDocuments {
			result.Document = doc
		}
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].RelevanceScore > results[j].RelevanceScore
	})
	if req.TopN > 0 && req.TopN < len(results) {
		results = results[:req.TopN]
	}

	return &rerank.RerankResponse{
		Results: results,
		Usage:   core.Usage{},
	}, nil
}
