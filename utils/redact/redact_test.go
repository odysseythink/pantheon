package redact

import (
	"regexp"
	"strings"
	"testing"
)

func TestStringRedactsBearer(t *testing.T) {
	in := "Authorization: Bearer abc123XYZ789defghijklmnop"
	out := String(in)
	if !strings.Contains(out, "[REDACTED]") || strings.Contains(out, "abc123XYZ789defghij") {
		t.Fatalf("got %q", out)
	}
}

func TestStringRedactsAWSKey(t *testing.T) {
	out := String("key=AKIAIOSFODNN7EXAMPLE")
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("got %q", out)
	}
}

func TestStringRedactsAnthropicKey(t *testing.T) {
	out := String("token sk-ant-abcdefghijklmnopqrstuv")
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("got %q", out)
	}
}

func TestStringRedactsEmail(t *testing.T) {
	out := String("alice@example.com sent the file")
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("got %q", out)
	}
}

func TestWithExtraPattern(t *testing.T) {
	extra := append(append([]*regexp.Regexp{}, DefaultPatterns...),
		regexp.MustCompile(`internal-id-\d+`))
	out := With("user=alice@example.com id=internal-id-12345", extra)
	if strings.Contains(out, "alice@example.com") || strings.Contains(out, "internal-id-12345") {
		t.Fatalf("got %q", out)
	}
}

func TestStringNoMatchesPassthrough(t *testing.T) {
	in := "hello world, nothing to scrub here"
	if out := String(in); out != in {
		t.Fatalf("got %q want %q", out, in)
	}
}
