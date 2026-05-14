// Package urlsafety holds a local allow/deny list for HTTP-fetch checks.
package urlsafety

import (
	"net/url"
	"strings"
	"sync"
)

// Policy is an allow/deny host policy. When no list is configured, the
// policy is fail-open (allows everything). Callers populate lists or
// route through an external safe-browsing service for network checks.
type Policy struct {
	mu    sync.RWMutex
	deny  map[string]bool
	allow map[string]bool
}

// New constructs a Policy with normalized host entries.
func New(denyHosts, allowHosts []string) *Policy {
	p := &Policy{deny: map[string]bool{}, allow: map[string]bool{}}
	for _, h := range denyHosts {
		p.deny[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for _, h := range allowHosts {
		p.allow[strings.ToLower(strings.TrimSpace(h))] = true
	}
	return p
}

// Check returns (safe, reason). Unknown hosts are considered safe
// when no allowlist is configured; otherwise only allowlisted hosts
// are safe.
func (p *Policy) Check(rawURL string) (bool, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, "invalid url"
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false, "missing host"
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.deny[host] {
		return false, "host is denylisted"
	}
	if len(p.allow) > 0 && !p.allow[host] {
		return false, "host not on allowlist"
	}
	return true, ""
}
