package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestCounterIncAndScrape(t *testing.T) {
	reg := NewRegistry()
	c := reg.NewCounter("gateway_messages_total", "Total inbound messages.")
	c.With(map[string]string{"platform": "telegram"}).Inc()
	c.With(map[string]string{"platform": "telegram"}).Add(2)
	c.With(map[string]string{"platform": "api_server"}).Inc()

	var buf bytes.Buffer
	reg.WriteTo(&buf)
	out := buf.String()

	if !strings.Contains(out, "# HELP gateway_messages_total Total inbound messages.") {
		t.Errorf("missing HELP: %s", out)
	}
	if !strings.Contains(out, "# TYPE gateway_messages_total counter") {
		t.Errorf("missing TYPE: %s", out)
	}
	if !strings.Contains(out, `gateway_messages_total{platform="telegram"} 3`) {
		t.Errorf("missing telegram=3: %s", out)
	}
	if !strings.Contains(out, `gateway_messages_total{platform="api_server"} 1`) {
		t.Errorf("missing api_server=1: %s", out)
	}
}

func TestCounterIdempotentRegistration(t *testing.T) {
	reg := NewRegistry()
	a := reg.NewCounter("c", "help")
	b := reg.NewCounter("c", "help")
	if a != b {
		t.Error("expected same counter pointer on repeat registration")
	}
}

func TestHistogramObserveAndScrape(t *testing.T) {
	reg := NewRegistry()
	h := reg.NewHistogram("gateway_handler_duration_seconds", "Handler latency.")
	h.With(map[string]string{"platform": "fake"}).Observe(0.05)
	h.With(map[string]string{"platform": "fake"}).Observe(0.15)
	h.With(map[string]string{"platform": "fake"}).Observe(2.0)

	var buf bytes.Buffer
	reg.WriteTo(&buf)
	out := buf.String()

	if !strings.Contains(out, "# TYPE gateway_handler_duration_seconds histogram") {
		t.Errorf("missing histogram TYPE: %s", out)
	}
	// 0.05 falls into le=0.05, le=0.1, le=0.25, ... buckets
	if !strings.Contains(out, `gateway_handler_duration_seconds_bucket{platform="fake",le="0.05"} 1`) {
		t.Errorf("missing le=0.05 count of 1: %s", out)
	}
	if !strings.Contains(out, `gateway_handler_duration_seconds_bucket{platform="fake",le="0.25"} 2`) {
		t.Errorf("missing le=0.25 count of 2: %s", out)
	}
	if !strings.Contains(out, `gateway_handler_duration_seconds_count{platform="fake"} 3`) {
		t.Errorf("missing count=3: %s", out)
	}
	if !strings.Contains(out, `gateway_handler_duration_seconds_bucket{platform="fake",le="+Inf"} 3`) {
		t.Errorf("missing +Inf bucket: %s", out)
	}
}

func TestCounterEmptyLabels(t *testing.T) {
	reg := NewRegistry()
	c := reg.NewCounter("plain", "plain counter")
	c.With(nil).Add(5)
	var buf bytes.Buffer
	reg.WriteTo(&buf)
	if !strings.Contains(buf.String(), "plain 5") {
		t.Errorf("expected 'plain 5', got %s", buf.String())
	}
}
