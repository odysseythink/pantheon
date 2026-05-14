package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odysseythink/pantheon/extensions/embed"
)

type cachedSkill struct {
	content string
	vector  []float32
	mtime   time.Time
}

// Retriever loads .md skill files from skillDir, searches them by
// substring match (FTS fallback), and optionally re-ranks by cosine
// similarity when an embedder is provided.
type Retriever struct {
	skillDir string
	embedder Embedder // nil = substring match only

	mu    sync.Mutex
	cache map[string]*cachedSkill // keyed by file path
}

// NewRetriever constructs a Retriever.
// embedder may be nil — falls back to case-insensitive substring match.
func NewRetriever(skillDir string, emb Embedder) *Retriever {
	return &Retriever{
		skillDir: skillDir,
		embedder: emb,
		cache:    make(map[string]*cachedSkill),
	}
}

// Retrieve returns the top-k most relevant skill contents for the given query.
func (r *Retriever) Retrieve(ctx context.Context, query string, k int) ([]string, error) {
	if k <= 0 {
		k = 3
	}
	entries, err := os.ReadDir(r.skillDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var skills []*cachedSkill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(r.skillDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		mtime := info.ModTime()

		cached, ok := r.cache[path]
		if !ok || !cached.mtime.Equal(mtime) {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			cached = &cachedSkill{content: string(data), mtime: mtime}
			if r.embedder != nil {
				v, err := r.embedder.Embed(ctx, cached.content)
				if err == nil {
					cached.vector = v
				}
			}
			r.cache[path] = cached
		}
		skills = append(skills, cached)
	}

	if len(skills) == 0 {
		return nil, nil
	}

	// If no embedder, use substring match.
	if r.embedder == nil {
		var matched []string
		lower := strings.ToLower(query)
		for _, s := range skills {
			if strings.Contains(strings.ToLower(s.content), lower) {
				matched = append(matched, s.content)
				if len(matched) >= k {
					break
				}
			}
		}
		return matched, nil
	}

	// Embed query and cosine rerank.
	qVec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		// Fallback to substring match on embedding failure.
		var matched []string
		lower := strings.ToLower(query)
		for _, s := range skills {
			if strings.Contains(strings.ToLower(s.content), lower) {
				matched = append(matched, s.content)
				if len(matched) >= k {
					break
				}
			}
		}
		return matched, nil
	}

	ranked := embed.Rerank(skills, func(s *cachedSkill) []float32 { return s.vector }, qVec)
	out := make([]string, 0, k)
	for i, item := range ranked {
		if i >= k {
			break
		}
		out = append(out, item.Value.content)
	}
	return out, nil
}
