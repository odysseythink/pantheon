package retry

import (
	"net/http"
	"testing"
	"time"
)

func h(key, value string) http.Header {
	hh := http.Header{}
	hh.Set(key, value)
	return hh
}

func TestHeaderDelay(t *testing.T) {
	now := time.Now()
	future := now.Add(5 * time.Second).Format(time.RFC1123)
	past := now.Add(-5 * time.Second).Format(time.RFC1123)

	tests := []struct {
		name     string
		headers  http.Header
		fallback time.Duration
		want     time.Duration
	}{
		{"retry-after-ms valid", h("retry-after-ms", "1500"), 1 * time.Second, 1500 * time.Millisecond},
		{"retry-after seconds", h("retry-after", "3"), 1 * time.Second, 3 * time.Second},
		{"retry-after RFC1123 future", h("retry-after", future), 1 * time.Second, 5 * time.Second},
		{"retry-after RFC1123 past", h("retry-after", past), 1 * time.Second, 1 * time.Second},
		{"retry-after-ms unreasonable (>60s)", h("retry-after-ms", "120000"), 2 * time.Second, 2 * time.Second},
		{"retry-after unreasonable (>60s)", h("retry-after", "120"), 2 * time.Second, 2 * time.Second},
		{"x-ratelimit-reset-requests valid", h("x-ratelimit-reset-requests", "5"), 1 * time.Second, 5 * time.Second},
		{"x-ratelimit-reset-tokens valid", h("x-ratelimit-reset-tokens", "3"), 1 * time.Second, 3 * time.Second},
		{"x-ratelimit-reset unreasonable", h("x-ratelimit-reset-requests", "120"), 2 * time.Second, 2 * time.Second},
		{"preemptive slowdown remaining-requests=1", h("x-ratelimit-remaining-requests", "1"), 2 * time.Second, 3 * time.Second},
		{"preemptive slowdown remaining-tokens=2", h("x-ratelimit-remaining-tokens", "2"), 2 * time.Second, 3 * time.Second},
		{"preemptive slowdown remaining=3 no effect", h("x-ratelimit-remaining-requests", "3"), 2 * time.Second, 2 * time.Second},
		{"retry-after-ms takes priority over retry-after", func() http.Header {
			hh := h("retry-after-ms", "500")
			hh.Set("retry-after", "10")
			return hh
		}(), 1 * time.Second, 500 * time.Millisecond},
		{"retry-after takes priority over ratelimit-reset", func() http.Header {
			hh := h("retry-after", "2")
			hh.Set("x-ratelimit-reset-requests", "10")
			return hh
		}(), 1 * time.Second, 2 * time.Second},
		// Test that delay >60s falls back even when fallback is larger
		{"delay >60s always fallback", h("retry-after", "70"), 120 * time.Second, 120 * time.Second},
		// Test remaining=0 triggers slowdown
		{"preemptive slowdown remaining=0", h("x-ratelimit-remaining-requests", "0"), 2 * time.Second, 3 * time.Second},
		// Test invalid header values are ignored
		{"invalid retry-after-ms", h("retry-after-ms", "abc"), 2 * time.Second, 2 * time.Second},
		// Test both remaining headers present (only first match applies)
		{"both remaining headers first wins", func() http.Header {
			hh := h("x-ratelimit-remaining-requests", "1")
			hh.Set("x-ratelimit-remaining-tokens", "1")
			return hh
		}(), 2 * time.Second, 3 * time.Second},
		{"no headers uses fallback", nil, 2 * time.Second, 2 * time.Second},
		{"empty headers uses fallback", http.Header{}, 2 * time.Second, 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := headerDelay(tt.headers, tt.fallback)
			diff := got - tt.want
			if diff < 0 {
				diff = -diff
			}
			if diff > 1*time.Second {
				t.Errorf("headerDelay() = %v, want %v (diff %v)", got, tt.want, diff)
			}
		})
	}
}
