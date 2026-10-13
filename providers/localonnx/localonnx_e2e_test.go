package localonnx

// On-device E2E tests. They skip unless ODYP_ONNX_POC_ROOT points at a
// directory laid out like the real data root:
//
//	$ODYP_ONNX_POC_ROOT/onnxruntime/onnxruntime.dll
//	$ODYP_ONNX_POC_ROOT/embedding_models/models--Qdrant--bge-small-zh-v1.5/snapshots/cos-mirror/
//	$ODYP_ONNX_POC_ROOT/tokenizer_golden.json          (optional)
//
// The golden file maps sample texts to token ids produced by the HF
// `tokenizers` library (the same engine fastembed uses), pinning the Go
// tokenizer to byte-identical output.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func pocRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("ODYBOX_ONNX_RUNTIME_DIR", "")
	root := os.Getenv("ODYP_ONNX_POC_ROOT")
	if root == "" {
		t.Skip("ODYP_ONNX_POC_ROOT not set")
	}
	if _, err := os.Stat(RuntimeLibPath(root)); err != nil {
		t.Skipf("onnxruntime library missing under %s", root)
	}
	return root
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

func TestTokenizerGolden(t *testing.T) {
	root := pocRoot(t)
	goldenPath := filepath.Join(root, "tokenizer_golden.json")
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Skip("no tokenizer_golden.json")
	}
	var golden map[string][]int
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	m, _ := Find("BAAI/bge-small-zh-v1.5")
	dir, err := resolveSnapshotDir(root, m)
	if err != nil {
		t.Fatalf("snapshot dir: %v", err)
	}
	tok, err := LoadTokenizer(dir)
	if err != nil {
		t.Fatalf("load tokenizer: %v", err)
	}
	for text, want := range golden {
		ids, _ := tok.Encode(text)
		if len(ids) != len(want) {
			t.Errorf("token count for %q: got %d want %d\n got=%v want=%v", text, len(ids), len(want), ids, want)
			continue
		}
		for i := range ids {
			if ids[i] != want[i] {
				t.Errorf("token %d for %q: got %d want %d (full got=%v want=%v)", i, text, ids[i], want[i], ids, want)
				break
			}
		}
	}
}

func TestEmbedRealModel(t *testing.T) {
	root := pocRoot(t)
	vecs, err := EmbedTexts(root, "BAAI/bge-small-zh-v1.5", []string{
		"今天天气怎么样？",
		"今天天气很好。",
		"量子计算机的运行原理涉及量子比特的叠加态。",
	})
	if err != nil {
		t.Fatalf("EmbedTexts: %v", err)
	}
	if len(vecs) != 3 || len(vecs[0]) != 512 {
		t.Fatalf("unexpected shapes: %d vectors, dim %d", len(vecs), len(vecs[0]))
	}
	same := cosine(vecs[0], vecs[1])
	unrelated := cosine(vecs[0], vecs[2])
	t.Logf("cosine(same-topic)=%.4f cosine(unrelated)=%.4f", same, unrelated)
	if same < 0.6 {
		t.Errorf("semantically close texts should score high, got %.4f", same)
	}
	if unrelated >= same {
		t.Errorf("unrelated pair (%.4f) should score below related pair (%.4f)", unrelated, same)
	}
	for _, v := range vecs {
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		if math.Abs(norm-1) > 1e-3 {
			t.Errorf("vector not L2-normalized: norm^2=%f", norm)
		}
	}
}
