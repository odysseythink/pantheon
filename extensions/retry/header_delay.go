package retry

import (
	"net/http"
	"strconv"
	"time"
)

// headerDelay extracts the optimal retry delay from HTTP response headers.
// Priority (highest to lowest):
//   1. retry-after-ms      — millisecond precision, used by OpenAI etc.
//   2. retry-after         — standard HTTP, seconds or RFC1123 date
//   3. x-ratelimit-reset-* — seconds until rate limit resets
//   4. fallback            — caller's exponential backoff delay
//
// If the header-derived delay is zero or exceeds 60 seconds, the fallback
// is used instead.
func headerDelay(headers http.Header, fallback time.Duration) time.Duration {
	if headers == nil {
		return fallback
	}

	var delay time.Duration

	// Priority 1: retry-after-ms (most precise)
	if v := headers.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			delay = time.Duration(ms * float64(time.Millisecond))
		}
	}

	// Priority 2: retry-after (seconds or RFC1123 date)
	if delay == 0 {
		if v := headers.Get("retry-after"); v != "" {
			if sec, err := strconv.ParseFloat(v, 64); err == nil {
				delay = time.Duration(sec * float64(time.Second))
			} else if t, err := time.Parse(time.RFC1123, v); err == nil {
				delay = time.Until(t)
				if delay < 0 {
					delay = 0
				}
			}
		}
	}

	// Priority 3: x-ratelimit-reset-requests / x-ratelimit-reset-tokens
	if delay == 0 {
		for _, key := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
			if v := headers.Get(key); v != "" {
				if sec, err := strconv.ParseFloat(v, 64); err == nil {
					delay = time.Duration(sec * float64(time.Second))
					break
				}
			}
		}
	}

	// Preemptive slowdown: if remaining quota is very low, increase fallback
	for _, key := range []string{"x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens"} {
		if v := headers.Get(key); v != "" {
			if n, err := strconv.ParseFloat(v, 64); err == nil && n >= 0 && n <= 2 {
				fallback = time.Duration(float64(fallback) * 1.5)
				break
			}
		}
	}

	// Sanity check: use header delay only if it's reasonable
	if delay > 0 && delay < 60*time.Second {
		return delay
	}

	return fallback
}
