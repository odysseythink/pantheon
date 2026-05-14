package mcpoauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("grant_type") != "client_credentials" {
			t.Fatalf("grant_type=%s", r.PostForm.Get("grant_type"))
		}
		if r.PostForm.Get("client_id") != "app" {
			t.Fatalf("client_id=%s", r.PostForm.Get("client_id"))
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "app", "secret", "read")
	tok, err := c.FetchToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok" {
		t.Fatalf("got %q want tok", tok)
	}
}

func TestFetchTokenMissingFields(t *testing.T) {
	c := New("", "", "", "")
	if _, err := c.FetchToken(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("got %v want missing-fields error", err)
	}
}

func TestFetchTokenServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("internal"))
	}))
	defer srv.Close()
	c := New(srv.URL, "a", "b", "")
	if _, err := c.FetchToken(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
