package osv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"name":"lodash"`) {
			t.Errorf("bad body: %s", body)
		}
		_, _ = w.Write([]byte(`{"vulns":[{"id":"CVE-2020-1","summary":"issue"}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	vulns, err := c.Query(context.Background(), "npm", "lodash", "4.17.15")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(vulns) != 1 || !strings.Contains(vulns[0], "CVE-2020-1") {
		t.Errorf("unexpected vulns: %v", vulns)
	}
}

func TestQueryNoVulns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"vulns":[]}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	vulns, err := c.Query(context.Background(), "npm", "safe-pkg", "1.0.0")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(vulns) != 0 {
		t.Errorf("expected no vulns, got %v", vulns)
	}
}

func TestQueryServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if _, err := c.Query(context.Background(), "a", "b", "c"); err == nil {
		t.Fatal("expected error")
	}
}
