// Package metrics provides a minimal in-process metrics registry
// that exposes its data in Prometheus text exposition format. The
// goal is to cover the small subset of functionality the hermes
// gateway and cron scheduler need (counters + histograms) without
// pulling in the prometheus/client_golang dependency.
//
// Usage:
//
//	reg := metrics.NewRegistry()
//	msgs := reg.NewCounter("gateway_messages_total", "Total inbound messages.")
//	msgs.With(map[string]string{"platform":"telegram"}).Inc()
//
//	http.Handle("/metrics", reg)
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// defaultBuckets is the histogram ladder shared by every Histogram
// created via Registry.NewHistogram.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry stores all counters and histograms in the process and
// implements http.Handler for the /metrics endpoint.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	histograms map[string]*Histogram
}

// NewRegistry builds an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		histograms: make(map[string]*Histogram),
	}
}

// NewCounter creates and registers a counter. Calling it twice with
// the same name returns the existing counter.
func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.counters[name]; ok {
		return existing
	}
	c := &Counter{name: name, help: help, values: make(map[string]float64)}
	r.counters[name] = c
	return c
}

// NewHistogram creates and registers a histogram with the default bucket ladder.
func (r *Registry) NewHistogram(name, help string) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.histograms[name]; ok {
		return existing
	}
	h := &Histogram{
		name:    name,
		help:    help,
		buckets: defaultBuckets,
		values:  make(map[string]*histogramState),
	}
	r.histograms[name] = h
	return h
}

// ServeHTTP writes the Prometheus text exposition format to w.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	r.WriteTo(w)
}

// WriteTo dumps metrics to any io.Writer. Tests use this to check
// output without an HTTP round-trip.
func (r *Registry) WriteTo(w io.Writer) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Counters, deterministic order.
	names := make([]string, 0, len(r.counters))
	for k := range r.counters {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		r.counters[n].writeTo(w)
	}

	// Histograms.
	names = names[:0]
	for k := range r.histograms {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		r.histograms[n].writeTo(w)
	}
}

// Counter is a monotonically increasing numeric value, keyed by
// optional label sets.
type Counter struct {
	name   string
	help   string
	mu     sync.Mutex
	values map[string]float64 // key -> value, key encodes label set
}

// Labelled returns a *Counter instance scoped to the given labels.
// Passing nil returns the base counter (no labels).
type counterHandle struct {
	parent *Counter
	labels map[string]string
}

// With returns a handle pre-bound to the given labels.
func (c *Counter) With(labels map[string]string) *counterHandle {
	return &counterHandle{parent: c, labels: labels}
}

// Inc adds 1 to the counter.
func (h *counterHandle) Inc() {
	h.Add(1)
}

// Add increments by n (should be >= 0).
func (h *counterHandle) Add(n float64) {
	if n < 0 {
		return
	}
	key := encodeLabels(h.labels)
	h.parent.mu.Lock()
	h.parent.values[key] += n
	h.parent.mu.Unlock()
}

func (c *Counter) writeTo(w io.Writer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name)
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s%s %s\n", c.name, k, formatFloat(c.values[k]))
	}
}

// Histogram tracks observations in fixed buckets plus a running sum
// and count. Keyed by optional label sets.
type Histogram struct {
	name    string
	help    string
	buckets []float64
	mu      sync.Mutex
	values  map[string]*histogramState
}

type histogramState struct {
	counts []uint64 // len == len(buckets), each tracks cumulative count up to and including bucket
	sum    float64
	count  uint64
}

// With returns a handle bound to labels.
func (h *Histogram) With(labels map[string]string) *histogramHandle {
	return &histogramHandle{parent: h, labels: labels}
}

type histogramHandle struct {
	parent *Histogram
	labels map[string]string
}

// Observe records a single sample value.
func (h *histogramHandle) Observe(v float64) {
	key := encodeLabels(h.labels)
	h.parent.mu.Lock()
	st, ok := h.parent.values[key]
	if !ok {
		st = &histogramState{counts: make([]uint64, len(h.parent.buckets))}
		h.parent.values[key] = st
	}
	st.sum += v
	st.count++
	for i, b := range h.parent.buckets {
		if v <= b {
			st.counts[i]++
		}
	}
	h.parent.mu.Unlock()
}

func (h *Histogram) writeTo(w io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
	keys := make([]string, 0, len(h.values))
	for k := range h.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st := h.values[k]
		baseLabels := decodeLabelsForBucket(k)
		for i, b := range h.buckets {
			label := appendLabel(baseLabels, "le", formatFloat(b))
			fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, label, st.counts[i])
		}
		infLabel := appendLabel(baseLabels, "le", "+Inf")
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, infLabel, st.count)
		fmt.Fprintf(w, "%s_sum%s %s\n", h.name, k, formatFloat(st.sum))
		fmt.Fprintf(w, "%s_count%s %d\n", h.name, k, st.count)
	}
}

// --- helpers ---------------------------------------------------------

// encodeLabels produces a canonical `{k1="v1",k2="v2"}` string (or
// empty string if labels is nil/empty).
func encodeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[k]))
		b.WriteString(`"`)
	}
	b.WriteString("}")
	return b.String()
}

// decodeLabelsForBucket returns the key unchanged (the caller already
// has a `{...}` string) or "{}" when it was empty, so that appendLabel
// can splice an extra `le` label into it.
func decodeLabelsForBucket(key string) string {
	if key == "" {
		return ""
	}
	return key
}

// appendLabel splices a new label into a canonical label string.
// base is either "" or "{k=v,...}".
func appendLabel(base, k, v string) string {
	if base == "" {
		return "{" + k + `="` + escapeLabelValue(v) + `"}`
	}
	// Remove trailing '}', append ",k=v}".
	inner := strings.TrimSuffix(base, "}")
	return inner + "," + k + `="` + escapeLabelValue(v) + `"}`
}

func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
