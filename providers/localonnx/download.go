package localonnx

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Download sources, ported from onnx_download.py: a public COS bucket wins
// when reachable, then Hugging Face, then the hf-mirror. Every base is
// overridable for tests and private deployments.
var (
	cosBase = func() string {
		if v := strings.TrimSpace(os.Getenv("ODYBOX_ONNX_COS_BASE")); v != "" {
			return strings.TrimRight(v, "/")
		}
		return "https://octop-1258344699.cos.ap-guangzhou.myqcloud.com"
	}()
	hfOfficial = "https://huggingface.co"
	hfMirror   = "https://hf-mirror.com"
	// Python installs the ONNX Runtime as a pip wheel; the Go build fetches
	// the official Microsoft release archive instead.
	runtimeReleaseBase = "https://github.com/microsoft/onnxruntime/releases/download"
	runtimeORTVersion  = "1.30.0"
)

const (
	cosPrefix      = "models/embedding"
	cosManifest    = "files.json"
	cosRevision    = "cos-mirror"
	probeTimeout   = 4 * time.Second
	dlUserAgent    = "odybox-onnx-download"
	markDownloaded = true
)

// SetRuntimeDownloadOverrides lets tests and deployments repoint the runtime
// download. Empty strings keep the current values.
func SetRuntimeDownloadOverride(baseURL, version string) {
	if baseURL != "" {
		runtimeReleaseBase = strings.TrimRight(baseURL, "/")
	}
	if version != "" {
		runtimeORTVersion = version
	}
}

// ProgressFn receives bytes downloaded so far; total is the best-known
// estimate and may be zero while unknown.
type ProgressFn func(downloaded int64, total int64)

type downloadCandidate struct {
	kind       string // cos | hf | hf-mirror
	probeURL   string
	hfEndpoint string
	hfRepo     string
	modelID    string
}

// hfRepoForModel ports _hf_repo_id: catalog hf_source, else the model id.
func hfRepoForModel(m Model) string {
	if m.HFSource != "" {
		return m.HFSource
	}
	return m.ID
}

func buildCandidates(m Model) []downloadCandidate {
	repo := hfRepoForModel(m)
	probe := "config.json"
	return []downloadCandidate{
		{kind: "cos",
			probeURL: cosFileURL(m.ID, probe), hfRepo: repo, modelID: m.ID},
		{kind: "hf",
			probeURL:   hfOfficial + "/" + repo + "/resolve/main/" + probe,
			hfEndpoint: hfOfficial, hfRepo: repo, modelID: m.ID},
		{kind: "hf-mirror",
			probeURL:   hfMirror + "/" + repo + "/resolve/main/" + probe,
			hfEndpoint: hfMirror, hfRepo: repo, modelID: m.ID},
	}
}

func cosFileURL(modelID, rel string) string {
	key := cosPrefix + "/" + strings.Trim(modelID, "/")
	if rel != "" {
		key += "/" + strings.TrimLeft(rel, "/")
	}
	return cosBase + "/" + key
}

// probeSource returns the TTFB in ms for a 1 KiB range GET.
func probeSource(ctx context.Context, url string, client *http.Client) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", dlUserAgent)
	req.Header.Set("Range", "bytes=0-1023")
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("probe %s: HTTP %d", url, resp.StatusCode)
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 16)
	return time.Since(started).Milliseconds(), nil
}

// raceSources ports race_download_sources: probe all candidates in parallel,
// first success wins, the rest keep their order as fallbacks. When every
// probe fails the original order is returned so the caller still tries.
func raceSources(ctx context.Context, cands []downloadCandidate, client *http.Client) []downloadCandidate {
	type result struct {
		idx  int
		ttfb int64
	}
	results := make(chan result, len(cands))
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func(i int, c downloadCandidate) {
			defer wg.Done()
			ttfb, err := probeSource(cctx, c.probeURL, client)
			if err == nil {
				results <- result{idx: i, ttfb: ttfb}
			}
		}(i, c)
	}
	go func() { wg.Wait(); close(results) }()
	bestIdx, bestTTFB := -1, int64(-1)
	for r := range results {
		if bestIdx < 0 || r.ttfb < bestTTFB {
			bestIdx, bestTTFB = r.idx, r.ttfb
		}
	}
	if bestIdx < 0 {
		return append([]downloadCandidate(nil), cands...)
	}
	ordered := []downloadCandidate{cands[bestIdx]}
	for i, c := range cands {
		if i != bestIdx {
			ordered = append(ordered, c)
		}
	}
	return ordered
}

// allowPattern ports _HF_ALLOW_PATTERNS (simple fnmatch-style suffix/path
// matching over repo-relative paths).
func allowPattern(path string) bool {
	base := filepath.Base(path)
	lower := strings.ToLower(path)
	switch base {
	case "config.json", "tokenizer.json", "tokenizer_config.json",
		"special_tokens_map.json", "preprocessor_config.json",
		"vocab.txt", "vocab.json", "merges.txt", "sentencepiece.bpe.model":
		return true
	}
	for _, pat := range []string{".onnx", ".onnx_data"} {
		if strings.HasSuffix(lower, pat) {
			return true
		}
	}
	// onnx/*.onnx, onnx/*.onnx_data, onnx/*.json
	if strings.HasPrefix(path, "onnx/") &&
		(strings.HasSuffix(lower, ".onnx") || strings.HasSuffix(lower, ".onnx_data") ||
			strings.HasSuffix(lower, ".json")) {
		return true
	}
	return false
}

// cosManifestFiles fetches and validates files.json (ports _manifest_files
// with the same parent-escape rejections).
func cosManifestFiles(ctx context.Context, client *http.Client, modelID string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cosFileURL(modelID, cosManifest), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", dlUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("COS manifest: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Files []any `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("COS manifest: %w", err)
	}
	if len(payload.Files) == 0 {
		return nil, errors.New("COS manifest has no files")
	}
	var files []string
	for _, item := range payload.Files {
		switch v := item.(type) {
		case string:
			if rel, err := safeRelPath(v); err == nil {
				files = append(files, rel)
			} else {
				return nil, err
			}
		case map[string]any:
			path, _ := v["path"].(string)
			rel, err := safeRelPath(path)
			if err != nil {
				return nil, err
			}
			files = append(files, rel)
		default:
			return nil, fmt.Errorf("invalid COS manifest entry: %v", item)
		}
	}
	return files, nil
}

func safeRelPath(raw string) (string, error) {
	rel := strings.TrimLeft(strings.ReplaceAll(raw, "\\", "/"), "/")
	if rel == "" || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains("/"+rel+"/", "/../") {
		return "", fmt.Errorf("unsafe object path: %q", raw)
	}
	return rel, nil
}

// hfRepoFiles lists the repo files via the HF API.
func hfRepoFiles(ctx context.Context, client *http.Client, endpoint, repo string) ([]string, error) {
	url := endpoint + "/api/models/" + repo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", dlUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HF repo listing: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Siblings []struct {
			Filename string `json:"rfilename"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range payload.Siblings {
		if s.Filename != "" && allowPattern(s.Filename) {
			out = append(out, s.Filename)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("HF repo has no matching files")
	}
	return out, nil
}

// downloadFile streams url to dest (via .part) and reports progress.
func downloadFile(ctx context.Context, client *http.Client, url, dest string, onProgress ProgressFn, doneSoFar int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", dlUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	total := resp.ContentLength // may be -1
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	written := int64(0)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return written, werr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(doneSoFar+written, max64(total, 0))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return written, rerr
		}
	}
	if err := f.Close(); err != nil {
		return written, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return written, err
	}
	return written, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// writeHFRefMaterializes the refs/main pointer so both the Python build's
// huggingface_hub cache reader and ours resolve the snapshot.
func writeHFRef(cacheDir, repo, revision string) error {
	repoDir := filepath.Join(cacheDir, "models--"+strings.ReplaceAll(repo, "/", "--"))
	refDir := filepath.Join(repoDir, "refs")
	if err := os.MkdirAll(refDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(refDir, "main"), []byte(revision+"\n"), 0o644)
}

// DownloadModel ports download_model_raced: race COS/HF/hf-mirror, download
// from the winner into the HF cache layout under root, then mark downloaded.
// Returns the winning source kind.
func DownloadModel(ctx context.Context, root, modelID string, onProgress ProgressFn) (string, error) {
	m, ok := Find(modelID)
	if !ok {
		return "", fmt.Errorf("model not in ONNX catalog: %s", modelID)
	}
	if m.Tokenizer != TokenizerWordPiece {
		return "", fmt.Errorf("model %s uses the %q tokenizer, which is not supported in the Go build", m.ID, m.Tokenizer)
	}
	cacheDir := CacheDir(root)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 0} // per-request contexts bound the time
	var errs []string
	for _, cand := range raceSources(ctx, buildCandidates(m), client) {
		var err error
		switch cand.kind {
		case "cos":
			err = downloadCOS(ctx, client, cand, cacheDir, onProgress)
		default:
			err = downloadHF(ctx, client, cand, cacheDir, onProgress)
		}
		if err == nil {
			markModelDownloaded(root, m.ID)
			CloseSession(m.ID)
			return cand.kind, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", cand.kind, err))
	}
	if len(errs) == 0 {
		errs = append(errs, "no sources")
	}
	return "", fmt.Errorf("all embedding download sources failed: %s", strings.Join(errs, "; "))
}

// downloadCOS fetches the manifest and every listed file into
// models--{hf_repo}/snapshots/cos-mirror/ (the Python-compatible layout).
func downloadCOS(ctx context.Context, client *http.Client, cand downloadCandidate, cacheDir string, onProgress ProgressFn) error {
	files, err := cosManifestFiles(ctx, client, cand.modelID)
	if err != nil {
		return err
	}
	if err := writeHFRef(cacheDir, cand.hfRepo, cosRevision); err != nil {
		return err
	}
	dest := filepath.Join(cacheDir, "models--"+strings.ReplaceAll(cand.hfRepo, "/", "--"),
		"snapshots", cosRevision)
	var downloaded int64
	for _, rel := range files {
		written, err := downloadFile(ctx, client, cosFileURL(cand.modelID, rel), filepath.Join(dest, rel), onProgress, downloaded)
		if err != nil {
			return err
		}
		downloaded += written
	}
	if !HasONNXWeights(dest) {
		return fmt.Errorf("COS snapshot for %s has no ONNX weights", cand.modelID)
	}
	return nil
}

// downloadHF resolves the allow-listed files from one HF endpoint into
// models--{hf_repo}/snapshots/main/.
func downloadHF(ctx context.Context, client *http.Client, cand downloadCandidate, cacheDir string, onProgress ProgressFn) error {
	files, err := hfRepoFiles(ctx, client, cand.hfEndpoint, cand.hfRepo)
	if err != nil {
		return err
	}
	if err := writeHFRef(cacheDir, cand.hfRepo, "main"); err != nil {
		return err
	}
	dest := filepath.Join(cacheDir, "models--"+strings.ReplaceAll(cand.hfRepo, "/", "--"),
		"snapshots", "main")
	var downloaded int64
	for _, rel := range files {
		url := cand.hfEndpoint + "/" + cand.hfRepo + "/resolve/main/" + rel
		written, err := downloadFile(ctx, client, url, filepath.Join(dest, rel), onProgress, downloaded)
		if err != nil {
			return err
		}
		downloaded += written
	}
	if !HasONNXWeights(dest) {
		return fmt.Errorf("HF snapshot for %s has no ONNX weights", cand.modelID)
	}
	return nil
}

func markModelDownloaded(root, modelID string) {
	dir := filepath.Join(CacheDir(root), ".downloaded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, strings.ReplaceAll(modelID, "/", "--")), []byte(modelID), 0o644)
}

// runtimeMirrorPrefixes are tried in order after the direct GitHub URL.
// GitHub release downloads are frequently unreachable from CN networks
// (the Python build sidesteps this via pip/PyPI mirrors), so fall back to
// public GitHub proxies. Difference vs Python is documented on purpose.
var runtimeMirrorPrefixes = []string{"", "https://gh-proxy.com/", "https://ghproxy.net/"}

// runtimeArchiveURL builds the Microsoft release URL for this platform.
func runtimeArchiveURL() string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf("%s/v%[2]s/onnxruntime-win-x64-%[2]s.zip", runtimeReleaseBase, runtimeORTVersion)
	case "darwin":
		return fmt.Sprintf("%s/v%[2]s/onnxruntime-osx-arm64-%[2]s.tgz", runtimeReleaseBase, runtimeORTVersion)
	default:
		return fmt.Sprintf("%s/v%[2]s/onnxruntime-linux-x64-%[2]s.tgz", runtimeReleaseBase, runtimeORTVersion)
	}
}

// DownloadRuntime fetches the ONNX Runtime shared library into RuntimeDir.
// This is the Go-build equivalent of Python's `pip install onnxruntime`
// (ensure_local_embedding_deps with allow_install).
func DownloadRuntime(ctx context.Context, root string, onProgress ProgressFn) error {
	if DepsAvailable(root) {
		return nil
	}
	destDir := RuntimeDir(root)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	base := runtimeArchiveURL()
	var lastErr error
	for _, prefix := range runtimeMirrorPrefixes {
		err := fetchAndExtractRuntime(ctx, prefix+base, destDir, onProgress)
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return fmt.Errorf("all runtime sources failed, last error: %w", lastErr)
}

func fetchAndExtractRuntime(ctx context.Context, url, destDir string, onProgress ProgressFn) error {
	client := &http.Client{Timeout: 0}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", dlUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	total := max64(resp.ContentLength, 0)
	var downloaded int64
	count := func(n int64) {
		downloaded += n
		if onProgress != nil {
			onProgress(downloaded, total)
		}
	}

	if strings.HasSuffix(url, ".zip") {
		return extractRuntimeZip(resp.Body, destDir, count)
	}
	return extractRuntimeTarGz(resp.Body, destDir, count)
}

func extractRuntimeZip(r io.Reader, destDir string, count func(int64)) error {
	tmp := destDir + ".dl.zip"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	size, err := io.CopyBuffer(f, r, make([]byte, 256<<10))
	if err != nil {
		f.Close()
		return err
	}
	f.Close()
	count(size)
	zr, err := zip.OpenReader(tmp)
	if err != nil {
		return err
	}
	defer zr.Close()
	libName := runtimeLibName()
	extracted := false
	for _, zf := range zr.File {
		base := filepath.Base(zf.Name)
		if zf.FileInfo().IsDir() || (!strings.EqualFold(base, libName) &&
			!strings.HasPrefix(strings.ToLower(base), "onnxruntime_providers_shared")) {
			continue
		}
		if err := extractZipEntry(zf, filepath.Join(destDir, base)); err != nil {
			return err
		}
		if strings.EqualFold(base, libName) {
			extracted = true
		}
	}
	if !extracted {
		return fmt.Errorf("runtime archive does not contain %s", libName)
	}
	return nil
}

func extractZipEntry(zf *zip.File, dest string) error {
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func extractRuntimeTarGz(r io.Reader, destDir string, count func(int64)) error {
	// Stream through a temp file so gzip can be read twice (count + extract).
	tmp := destDir + ".dl.tgz"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	size, err := io.CopyBuffer(f, r, make([]byte, 256<<10))
	if err != nil {
		f.Close()
		return err
	}
	f.Close()
	count(size)

	gz, err := os.Open(tmp)
	if err != nil {
		return err
	}
	defer gz.Close()
	gzr, err := gzip.NewReader(gz)
	if err != nil {
		return err
	}
	defer gzr.Close()

	libName := runtimeLibName()
	soFiles := map[string]string{} // name -> temp path
	tr := tar.NewReader(gzr)
	extracted := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			continue // resolved below via the real file copy
		}
		base := filepath.Base(hdr.Name)
		if !strings.HasPrefix(base, "libonnxruntime") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, base)
		if err := os.WriteFile(dest, data, 0o755); err != nil {
			return err
		}
		soFiles[base] = dest
		if base == libName {
			extracted = true
		}
	}
	if extracted {
		return nil
	}
	// The official Linux archive ships libonnxruntime.so as a symlink to
	// libonnxruntime.so.x.y.z; materialize the unversioned name ourselves.
	var versioned []string
	for name := range soFiles {
		if name != libName {
			versioned = append(versioned, name)
		}
	}
	if len(versioned) == 0 {
		return fmt.Errorf("runtime archive does not contain %s", libName)
	}
	sort.Strings(versioned)
	src := soFiles[versioned[len(versioned)-1]]
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, libName), data, 0o755)
}
