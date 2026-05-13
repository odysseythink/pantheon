package catwalk

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/odysseythink/pantheon/core"
)

var catwalkBaseURL = "https://catwalk.charm.land"

// GetBaseURL returns the current catwalk base URL.
func GetBaseURL() string { return catwalkBaseURL }

// SetBaseURL sets the catwalk base URL. Returns the previous value.
func SetBaseURL(url string) string {
	prev := catwalkBaseURL
	catwalkBaseURL = url
	return prev
}

var (
	cacheData   []providerEntry
	cacheExpiry time.Time
	cacheMu     sync.RWMutex
	cacheTTL    = 5 * time.Minute
)

type providerEntry struct {
	ID     string       `json:"id"`
	Models []core.Model `json:"models"`
}

var providerIDMapping = map[string]string{
	"google": "gemini",
	"kimi":   "kimi-coding",
}

// ListModels returns the list of models for the given provider.
// It tries catwalk first (with caching), then falls back to the vendor API.
func ListModels(ctx context.Context, providerName, apiKey, baseURL string) ([]core.Model, error) {
	models, err := listFromCatwalk(ctx, providerName)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	return fallbackToProvider(ctx, providerName, apiKey, baseURL)
}

func listFromCatwalk(ctx context.Context, providerName string) ([]core.Model, error) {
	cacheMu.RLock()
	if time.Now().Before(cacheExpiry) && len(cacheData) > 0 {
		defer cacheMu.RUnlock()
		return matchProvider(cacheData, providerName)
	}
	cacheMu.RUnlock()

	cacheMu.Lock()
	defer cacheMu.Unlock()

	if time.Now().Before(cacheExpiry) && len(cacheData) > 0 {
		return matchProvider(cacheData, providerName)
	}

	entries, err := fetchCatwalk(ctx)
	if err != nil {
		return nil, err
	}
	cacheData = entries
	cacheExpiry = time.Now().Add(cacheTTL)

	return matchProvider(cacheData, providerName)
}

func fetchCatwalk(ctx context.Context) ([]providerEntry, error) {
	url := catwalkBaseURL + "/v2/providers"
	entries, err := core.HttpClientCall[[]providerEntry](ctx, http.MethodGet, url, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("catwalk: %w", ErrCatwalkUnavailable)
	}
	return entries, nil
}

func matchProvider(entries []providerEntry, providerName string) ([]core.Model, error) {
	catwalkID, ok := providerIDMapping[providerName]
	if !ok {
		catwalkID = providerName
	}
	for _, entry := range entries {
		if entry.ID == catwalkID {
			if len(entry.Models) > 0 {
				return entry.Models, nil
			}
			return nil, ErrProviderNotFound
		}
	}
	return nil, ErrProviderNotFound
}
