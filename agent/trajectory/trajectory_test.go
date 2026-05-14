package trajectory

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

func TestWriteAppends(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(Event{Kind: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(Event{Kind: "assistant", Content: "hello", Usage: &core.Usage{PromptTokens: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, "sess1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("line %d: %v", lines, err)
		}
		if ev.Time.IsZero() {
			t.Fatalf("line %d: time not filled", lines)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("got %d lines want 2", lines)
	}
}

func TestNewCreatesNestedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	w, err := New(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}

func TestWritePreservesTime(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tm := time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)
	if err := w.Write(Event{Time: tm, Kind: "user"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(w.Path())
	if !json.Valid(raw[:len(raw)-1]) {
		t.Fatal("invalid json")
	}
}

func TestCloseIdempotent(t *testing.T) {
	w, err := New(t.TempDir(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
