package localonnx

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Runtime library file names per platform (Python ships them inside the
// onnxruntime pip package; the Go build keeps them under RuntimeDir).
func runtimeLibName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.dylib"
	default:
		return "libonnxruntime.so"
	}
}

// RuntimeLibPath is where this build expects the ONNX Runtime shared library.
func RuntimeLibPath(root string) string {
	return filepath.Join(RuntimeDir(root), runtimeLibName())
}

// DepsAvailable reports whether the ONNX Runtime library file exists.
// (Initialization failures are reported separately by EmbedTexts.)
func DepsAvailable(root string) bool {
	_, err := os.Stat(RuntimeLibPath(root))
	return err == nil
}

var (
	initOnce sync.Once
	initErr  error
)

func ensureRuntime(root string) error {
	lib := RuntimeLibPath(root)
	if _, err := os.Stat(lib); err != nil {
		return fmt.Errorf("onnxruntime library not found at %s (download any model from the local models panel to fetch it)", lib)
	}
	initOnce.Do(func() {
		ortLibPath = lib
		initErr = ortInitialize(lib)
	})
	if initErr != nil {
		return fmt.Errorf("initialize onnxruntime: %w", initErr)
	}
	return nil
}

// sessionBundle holds one loaded model session and its tokenizer.
type sessionBundle struct {
	mu      sync.Mutex
	model   Model
	onnx    []byte
	session *ortSession
	inputs  []string
	outputs []string
	tok     *Tokenizer
}

var (
	sessMu    sync.Mutex
	sessions  = map[string]*sessionBundle{} // keyed by model id
	embedGate sync.Mutex                    // serializes inference (one shared OrtEnv; fastembed runs single-threaded per model too)
)

// resolveSnapshotDir picks the first cache directory that holds weights and
// returns the directory that directly contains them (the flat layout stores
// weights at the cache-dir root; the HF layout nests them under
// snapshots/<rev>/ — tokenizer.json sits alongside in both).
func resolveSnapshotDir(root string, m Model) (string, error) {
	for _, dir := range snapshotDirs(root, m) {
		if HasONNXWeights(dir) {
			var weightsDir string
			_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() || weightsDir != "" {
					if weightsDir != "" {
						return filepath.SkipAll
					}
					return nil //nolint:nilerr // continue walking
				}
				if strings.HasSuffix(strings.ToLower(path), ".onnx") {
					weightsDir = filepath.Dir(path)
					return filepath.SkipAll
				}
				return nil
			})
			if weightsDir != "" {
				return weightsDir, nil
			}
		}
	}
	return "", fmt.Errorf("model %s is not downloaded", m.ID)
}

// resolveWeights picks the primary ONNX file inside a snapshot directory:
// the catalog model_file when present, otherwise the largest *.onnx
// (mirrors fastembed, which also tolerates repos that only ship one variant).
func resolveWeights(dir string, m Model) (string, error) {
	if m.ModelFile != "" {
		for _, cand := range []string{
			filepath.Join(dir, m.ModelFile),
			filepath.Join(dir, filepath.FromSlash(m.ModelFile)),
		} {
			if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Size() > 0 {
				return cand, nil
			}
		}
	}
	var best string
	var bestSize int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".onnx") {
			return nil //nolint:nilerr // continue walking
		}
		if info.Size() > bestSize {
			best, bestSize = path, info.Size()
		}
		return nil
	})
	if best == "" {
		return "", fmt.Errorf("no .onnx weights found under %s", dir)
	}
	return best, nil
}

func getSession(root string, m Model) (*sessionBundle, error) {
	sessMu.Lock()
	defer sessMu.Unlock()
	if s, ok := sessions[m.ID]; ok {
		return s, nil
	}
	if err := ensureRuntime(root); err != nil {
		return nil, err
	}
	dir, err := resolveSnapshotDir(root, m)
	if err != nil {
		return nil, err
	}
	onnxPath, err := resolveWeights(dir, m)
	if err != nil {
		return nil, err
	}
	tok, err := LoadTokenizer(dir)
	if err != nil {
		return nil, err
	}
	model, err := os.ReadFile(onnxPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", onnxPath, err)
	}
	io, err := onnxGraphIONames(model)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", onnxPath, err)
	}
	session, err := ortCreateSession(model)
	if err != nil {
		return nil, err
	}
	s := &sessionBundle{
		model: m, onnx: model, session: session,
		inputs: io.Inputs, outputs: io.Outputs, tok: tok,
	}
	sessions[m.ID] = s
	return s, nil
}

// CloseSession drops the cached session for a model (after delete/download
// refreshes the weights on disk).
func CloseSession(modelID string) {
	sessMu.Lock()
	defer sessMu.Unlock()
	if s, ok := sessions[modelID]; ok {
		s.session.Destroy()
		delete(sessions, modelID)
	}
}

// CloseAll releases every cached session (used by tests and shutdown paths).
func CloseAll() {
	sessMu.Lock()
	defer sessMu.Unlock()
	for id, s := range sessions {
		s.session.Destroy()
		delete(sessions, id)
	}
}

// EmbedTexts runs the local model over texts, returning one L2-normalized
// vector per input (ports onnx_service.embed_texts).
func EmbedTexts(root string, modelID string, texts []string) ([][]float32, error) {
	m, ok := Find(modelID)
	if !ok {
		return nil, fmt.Errorf("model not in ONNX catalog: %s", modelID)
	}
	if m.Tokenizer != TokenizerWordPiece {
		return nil, fmt.Errorf("model %s uses the %q tokenizer, which is not supported in the Go build", m.ID, m.Tokenizer)
	}
	if err := ensureRuntime(root); err != nil {
		return nil, err
	}
	s, err := getSession(root, m)
	if err != nil {
		return nil, err
	}

	embedGate.Lock()
	defer embedGate.Unlock()
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		vec, err := s.embedOne(text)
		if err != nil {
			return nil, err
		}
		out = append(out, vec)
	}
	return out, nil
}

// embedOne tokenizes one text, runs the session and pools the output.
// The output tensor is preallocated (ORT's NULL-output auto-allocation is
// not reliable across runtimes): first as [1, seq, dim] for the BERT-style
// 3d outputs, retrying as [1, dim] for models that emit pooled vectors.
func (s *sessionBundle) embedOne(text string) ([]float32, error) {
	ids, mask := s.tok.Encode(text)
	seq := len(ids)

	idData := make([]int64, seq)
	maskData := make([]int64, seq)
	for i, id := range ids {
		idData[i] = int64(id)
		maskData[i] = int64(mask[i])
	}
	shape := []int64{1, int64(seq)}

	inputs := make([]ortTensor, 0, len(s.inputs))
	for _, name := range s.inputs {
		switch name {
		case "input_ids":
			t, err := ortMakeTensor(unsafe.Pointer(unsafe.SliceData(idData)), len(idData)*8, ortTypeInt64, shape)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, t)
		case "attention_mask":
			t, err := ortMakeTensor(unsafe.Pointer(unsafe.SliceData(maskData)), len(maskData)*8, ortTypeInt64, shape)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, t)
		case "token_type_ids":
			zeros := make([]int64, seq)
			t, err := ortMakeTensor(unsafe.Pointer(unsafe.SliceData(zeros)), len(zeros)*8, ortTypeInt64, shape)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, t)
		case "position_ids":
			pos := make([]int64, seq)
			for i := range pos {
				pos[i] = int64(i)
			}
			t, err := ortMakeTensor(unsafe.Pointer(unsafe.SliceData(pos)), len(pos)*8, ortTypeInt64, shape)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, t)
		default:
			return nil, fmt.Errorf("model input %q is not supported by the local embedding runner", name)
		}
	}
	runtime.KeepAlive(idData)
	runtime.KeepAlive(maskData)
	defer func() {
		for _, t := range inputs {
			t.release()
		}
	}()

	dim := int64(s.model.Dim)
	if dim <= 0 {
		dim = int64(defaultEmbeddingDim)
	}
	run := func(outShape []int64, outData []float32) error {
		outT, err := ortMakeTensor(unsafe.Pointer(unsafe.SliceData(outData)), len(outData)*4, ortTypeFloat32, outShape)
		if err != nil {
			return err
		}
		defer outT.release()
		outVals := []unsafe.Pointer{outT.ptr}
		return ortRun(s.session, s.inputs, s.outputs, inputs, outVals)
	}

	outShape := []int64{1, int64(seq), dim}
	outData := make([]float32, int64(seq)*dim)
	if err := run(outShape, outData); err != nil {
		// 2d-output models (already pooled at export time).
		outShape = []int64{1, dim}
		outData = make([]float32, dim)
		if err2 := run(outShape, outData); err2 != nil {
			return nil, fmt.Errorf("run %s: %w (also tried pooled shape: %v)", s.model.ID, err, err2)
		}
	}
	return poolOutput(outData, outShape, s.model.Pooling, maskData)
}

// defaultEmbeddingDim guards against a catalog entry missing its dim.
const defaultEmbeddingDim = 384

// poolOutput ports the two fastembed post-processors:
//   - PoolingCLS: a (batch, seq, dim) output yields embeddings[:, 0]; a 2d
//     output passes through (OnnxTextEmbedding._post_process_onnx_output)
//   - PoolingMean: attention-mask weighted mean over sequence positions
//     (PooledNormalizedEmbedding)
//
// Both L2-normalize the result.
func poolOutput(data []float32, shape []int64, kind PoolingKind, mask []int64) ([]float32, error) {
	var vec []float32
	switch {
	case len(shape) == 3:
		seqLen, dim := shape[1], shape[2]
		if seqLen <= 0 || dim <= 0 || int64(len(data)) != seqLen*dim {
			return nil, fmt.Errorf("unexpected output shape %v (data %d)", shape, len(data))
		}
		if kind == PoolingMean {
			vec = meanPool(data, seqLen, dim, mask)
		} else {
			vec = append([]float32(nil), data[:dim]...)
		}
	case len(shape) == 2:
		dim := shape[1]
		if dim <= 0 || int64(len(data)) != dim {
			return nil, fmt.Errorf("unexpected output shape %v (data %d)", shape, len(data))
		}
		vec = append([]float32(nil), data...)
	default:
		return nil, fmt.Errorf("unsupported output rank %d (shape %v)", len(shape), shape)
	}
	l2Normalize(vec)
	return vec, nil
}

func meanPool(data []float32, seqLen, dim int64, mask []int64) []float32 {
	vec := make([]float32, dim)
	var total float64
	for s := int64(0); s < seqLen; s++ {
		w := float64(1)
		if s < int64(len(mask)) {
			w = float64(mask[s])
		}
		total += w
		if w == 0 {
			continue
		}
		base := s * dim
		for d := int64(0); d < dim; d++ {
			vec[d] += data[base+d] * float32(w)
		}
	}
	if total > 0 {
		inv := float32(1 / total)
		for d := range vec {
			vec[d] *= inv
		}
	}
	return vec
}

func l2Normalize(vec []float32) {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum <= 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range vec {
		vec[i] *= inv
	}
}

// Probe runs a tiny embedding entirely on-device and reports the vector width
// (ports probe_local_model — no network access).
func Probe(root string, modelID string) (dim int, latencyMS int64, err error) {
	m, ok := Find(modelID)
	if !ok {
		return 0, 0, fmt.Errorf("model not in ONNX catalog: %s", modelID)
	}
	if !IsModelDownloaded(root, m) {
		return 0, 0, errors.New("model is not downloaded yet; download it before testing")
	}
	started := time.Now()
	vecs, err := EmbedTexts(root, modelID, []string{"ODYBOX onnx local probe"})
	if err != nil {
		return 0, 0, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, 0, errors.New("embedding returned no vectors")
	}
	return len(vecs[0]), time.Since(started).Milliseconds(), nil
}
