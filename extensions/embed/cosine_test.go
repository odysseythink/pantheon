package embed

import "testing"

func TestCosineIdentical(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if got := Cosine(a, b); got < 0.999 {
		t.Fatalf("got %v want ~1", got)
	}
}

func TestCosineOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	if got := Cosine(a, b); got > 0.001 {
		t.Fatalf("got %v want ~0", got)
	}
}

func TestCosineEmpty(t *testing.T) {
	if got := Cosine(nil, nil); got != 0 {
		t.Fatalf("got %v want 0", got)
	}
}

func TestCosineLengthMismatch(t *testing.T) {
	if got := Cosine([]float32{1, 2}, []float32{1, 2, 3}); got != 0 {
		t.Fatalf("got %v want 0", got)
	}
}

func TestRerankEmpty(t *testing.T) {
	scores := Rerank([]string{}, func(s string) []float32 { return nil }, []float32{1, 0, 0})
	if len(scores) != 0 {
		t.Fatalf("got %d want 0", len(scores))
	}
}

func TestRerankSortDescending(t *testing.T) {
	items := []string{"a", "b", "c"}
	vecs := map[string][]float32{
		"a": {0, 1, 0}, // orthogonal to query
		"b": {1, 0, 0}, // identical to query
		"c": {0.5, 0.5, 0},
	}
	ranked := Rerank(items, func(s string) []float32 { return vecs[s] }, []float32{1, 0, 0})
	if ranked[0].Value != "b" {
		t.Fatalf("top=%s want b", ranked[0].Value)
	}
	if ranked[2].Value != "a" {
		t.Fatalf("bottom=%s want a", ranked[2].Value)
	}
}
