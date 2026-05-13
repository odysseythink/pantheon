package catwalk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

func fallbackToProvider(ctx context.Context, providerName, apiKey, baseURL string) ([]core.Model, error) {
	switch providerName {
	case "openai", "deepseek", "ollama", "openrouter", "zhipu", "minimax", "kimi":
		return listOpenAIModels(ctx, apiKey, baseURL)
	case "anthropic":
		return listAnthropicModels(ctx, apiKey, baseURL)
	case "google":
		return listGoogleModels(ctx, apiKey, baseURL)
	case "qwen", "wenxin", "azure", "bedrock":
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catwalk fallback: status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catwalk fallback: status %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
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
	url := fmt.Sprintf("%s/v1beta/models?key=%s", strings.TrimSuffix(baseURL, "/"), apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catwalk fallback: status %d", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(result.Models))
	for _, m := range result.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, core.Model{ID: id, Name: id})
	}
	return models, nil
}
