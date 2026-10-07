package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRetrieverEmptyDir(t *testing.T) {
	dir := t.TempDir()
	r := NewRetriever(dir, nil)
	results, err := r.Retrieve(context.Background(), "anything", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty dir, got %d", len(results))
	}
}

func TestRetrieverFindsMatchingSkill(t *testing.T) {
	dir := t.TempDir()
	content := "## Git Reset\n**When to use:** When you need to undo commits\n\n1. Run git reset --hard HEAD"
	_ = os.WriteFile(filepath.Join(dir, "20260101-git-reset.md"), []byte(content), 0o644)

	r := NewRetriever(dir, nil)
	results, err := r.Retrieve(context.Background(), "git", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one result for 'git' query")
	}
}

func TestRetrieverReturnsAtMostK(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		_ = os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("skill-%d.md", i)),
			[]byte(fmt.Sprintf("## Skill %d\ncontent about topic", i)),
			0o644,
		)
	}
	r := NewRetriever(dir, nil)
	results, err := r.Retrieve(context.Background(), "topic", 2)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("want at most 2, got %d", len(results))
	}
}
