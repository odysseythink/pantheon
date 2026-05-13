package catwalk

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

func fallbackToProvider(ctx context.Context, providerName, apiKey, baseURL string) ([]core.Model, error) {
	switch providerName {
	case "openai", "deepseek", "ollama", "openrouter", "qwen", "wenxin", "zhipu", "minimax", "kimi":
		return listOpenAIModels(ctx, apiKey, baseURL)
	case "anthropic":
		return listAnthropicModels(ctx, apiKey, baseURL)
	case "google":
		return listGoogleModels(ctx, apiKey, baseURL)
	case "azure", "bedrock":
		return nil, fmt.Errorf("%w: %s", ErrProviderNotSupported, providerName)
	default:
		return nil, fmt.Errorf("%w: %s", ErrProviderNotSupported, providerName)
	}
}

func listOpenAIModels(ctx context.Context, apiKey, baseURL string) ([]core.Model, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("catwalk fallback: baseURL required for OpenAI-compatible provider")
	}
	url := strings.TrimSuffix(baseURL, "/") + "/models"
	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}

	result, err := core.HttpClientCall[struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}](ctx, http.MethodGet, url, nil, nil, headers)
	if err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(result.Data))
	for _, m := range result.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, core.Model{ID: m.ID, Name: name})
	}
	return models, nil
}

func listAnthropicModels(ctx context.Context, apiKey, baseURL string) ([]core.Model, error) {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	url := strings.TrimSuffix(baseURL, "/") + "/v1/models"
	headers := map[string]string{"anthropic-version": "2023-06-01"}
	if apiKey != "" {
		headers["x-api-key"] = apiKey
	}

	result, err := core.HttpClientCall[struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"display_name"`
		} `json:"data"`
	}](ctx, http.MethodGet, url, nil, nil, headers)
	if err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(result.Data))
	for _, m := range result.Data {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		models = append(models, core.Model{ID: m.ID, Name: name})
	}
	return models, nil
}

func listGoogleModels(ctx context.Context, apiKey, baseURL string) ([]core.Model, error) {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	url := strings.TrimSuffix(baseURL, "/") + "/v1beta/models"
	var query map[string][]string
	if apiKey != "" {
		query = map[string][]string{"key": {apiKey}}
	}

	result, err := core.HttpClientCall[struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}](ctx, http.MethodGet, url, query, nil, nil)
	if err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(result.Models))
	for _, m := range result.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, core.Model{ID: id, Name: id})
	}
	return models, nil
}
