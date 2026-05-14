// Package redact scrubs common secret patterns from arbitrary text.
// The default ruleset covers bearer tokens, AWS access keys,
// OpenAI/Anthropic-style API keys, OpenRouter keys, long hex tokens,
// password=/api_key=/token= assignments, and email addresses.
package redact

import "regexp"

// DefaultPatterns is the built-in ruleset. Exported so callers may
// inspect or append to it before passing into With.
var DefaultPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/=]{16,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`sk-(?:ant-|proj-|live-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`sk-or-v1-[A-Za-z0-9]{32,}`),
	regexp.MustCompile(`\b[a-f0-9]{32,64}\b`),
	regexp.MustCompile(`(?i)(password|api_key|apikey|token)\s*[:=]\s*["']?[^\s"']{8,}`),
	regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
}

// String replaces every match of DefaultPatterns in s with "[REDACTED]".
func String(s string) string {
	return With(s, DefaultPatterns)
}

// With replaces every match of patterns in s with "[REDACTED]".
// Callers can pass DefaultPatterns plus extra rules.
func With(s string, patterns []*regexp.Regexp) string {
	out := s
	for _, re := range patterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
