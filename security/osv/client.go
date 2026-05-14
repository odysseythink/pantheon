// Package osv talks to https://api.osv.dev to look up vulnerabilities
// for a given (ecosystem, package, version) tuple.
package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a small HTTP client for the OSV vulnerability database.
type Client struct {
	BaseURL string
	http    *http.Client
}

// New constructs a Client. baseURL defaults to https://api.osv.dev.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.osv.dev"
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 20 * time.Second}}
}

type osvRequest struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	Version string `json:"version,omitempty"`
}

type osvResponse struct {
	Vulns []struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"vulns"`
}

// Query returns the list of vulnerability IDs + summaries for a
// package in the given ecosystem at an optional version.
func (c *Client) Query(ctx context.Context, ecosystem, name, version string) ([]string, error) {
	req := osvRequest{Version: version}
	req.Package.Ecosystem = ecosystem
	req.Package.Name = name
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/query", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("osv: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("osv: status %d: %s", resp.StatusCode, string(errBody))
	}
	var out osvResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(out.Vulns))
	for _, v := range out.Vulns {
		lines = append(lines, v.ID+": "+v.Summary)
	}
	return lines, nil
}
