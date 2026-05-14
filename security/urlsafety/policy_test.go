package urlsafety

import "testing"

func TestCheckEmptyPolicy(t *testing.T) {
	p := New(nil, nil)
	safe, reason := p.Check("https://example.com")
	if !safe {
		t.Fatalf("expected safe, got %q", reason)
	}
}

func TestCheckDeny(t *testing.T) {
	p := New([]string{"evil.example.com"}, nil)
	safe, reason := p.Check("https://evil.example.com/foo")
	if safe {
		t.Fatal("expected unsafe")
	}
	if reason != "host is denylisted" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestCheckAllowlist(t *testing.T) {
	p := New(nil, []string{"example.com"})
	safe, _ := p.Check("https://example.com/x")
	if !safe {
		t.Error("example.com should be on allowlist")
	}
	safe, _ = p.Check("https://other.com/x")
	if safe {
		t.Error("other.com should be blocked when allowlist is set")
	}
}

func TestCheckInvalidURL(t *testing.T) {
	p := New(nil, nil)
	safe, reason := p.Check("://not-a-url")
	if safe {
		t.Error("invalid URL should not be safe")
	}
	if reason != "invalid url" {
		t.Errorf("reason = %q", reason)
	}
}

func TestCheckMissingHost(t *testing.T) {
	p := New(nil, nil)
	safe, reason := p.Check("file:///no-host")
	if safe {
		t.Error("URL without host should not be safe")
	}
	if reason != "missing host" {
		t.Errorf("reason = %q", reason)
	}
}
