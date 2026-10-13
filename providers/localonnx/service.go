package localonnx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ServiceConfig ports OnnxServiceConfig (same settings key and JSON shape
// for cross-implementation compatibility with the Python build).
type ServiceConfig struct {
	Enabled bool
	Model   string
}

// SettingsStore is the minimal settings access used here.
type SettingsStore interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

// SettingsKey is the shared settings key ("onnx_local_service").
const SettingsKey = "onnx_local_service"

// DefaultModelID ports _DEFAULT_MODEL (the first preset).
func DefaultModelID() string { return PresetModelIDs[0] }

// LoadConfig reads the service config; unknown/blank state yields the default.
func LoadConfig(st SettingsStore) ServiceConfig {
	cfg := ServiceConfig{Enabled: false, Model: DefaultModelID()}
	raw, _ := st.GetSetting(SettingsKey)
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	var data map[string]any
	if json.Unmarshal([]byte(raw), &data) != nil {
		return cfg
	}
	if v, ok := data["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := data["model"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.Model = strings.TrimSpace(v)
	}
	return cfg
}

// SaveConfig persists the service config.
func SaveConfig(st SettingsStore, cfg ServiceConfig) error {
	data, err := json.Marshal(map[string]any{"enabled": cfg.Enabled, "model": cfg.Model})
	if err != nil {
		return err
	}
	return st.SetSetting(SettingsKey, string(data))
}

// Download status values (port OnnxDownloadStatus).
const (
	StatusIdle        = "idle"
	StatusDownloading = "downloading"
	StatusLoading     = "loading"
	StatusDone        = "done"
	StatusFailed      = "failed"
)

// DownloadState is the polled download progress (port OnnxDownloadState; the
// JSON field names match OnnxDownloadState.to_dict).
type DownloadState struct {
	Status    string  `json:"status"`
	Progress  float64 `json:"progress"`
	Error     *string `json:"error"`
	ModelName string  `json:"model_name"`
	TaskID    string  `json:"task_id"`
}

// Manager serializes downloads (single active task, like OnnxDownloadManager)
// and owns the shared progress state. It must be process-wide singleton.
type Manager struct {
	mu    sync.Mutex
	state DownloadState
	root  string
	busy  bool

	// DownloadRuntimeFn / DownloadModelFn, when set (tests), replace the
	// real downloads.
	DownloadRuntimeFn func(ctx context.Context, root string, progress ProgressFn) error
	DownloadModelFn   func(ctx context.Context, root, modelID string, progress ProgressFn) (string, error)
}

// NewManager creates the manager for one data root.
func NewManager(root string) *Manager {
	return &Manager{root: root, state: DownloadState{Status: StatusIdle}}
}

// State snapshots the current download state.
func (m *Manager) State() DownloadState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) set(status string, progress float64, errMsg *string, model, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = DownloadState{
		Status: status, Progress: progress, Error: errMsg,
		ModelName: model, TaskID: taskID,
	}
}

// Start ports start_download: fail when another task runs, short-circuit when
// the model is already on disk (deps are ensured first, mirroring Python's
// ensure_local_embedding_deps(allow_install=True)), otherwise run the
// runtime+model download on a background goroutine.
func (m *Manager) Start(modelID string) (DownloadState, error) {
	model, ok := Find(modelID)
	if !ok {
		return m.State(), fmt.Errorf("model not in ONNX catalog: %s", modelID)
	}
	if model.Tokenizer != TokenizerWordPiece {
		return m.State(), fmt.Errorf("model %s uses the %q tokenizer, which is not supported in the Go build", model.ID, model.Tokenizer)
	}

	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return m.State(), fmt.Errorf("another ONNX download is already running")
	}
	m.busy = true
	m.mu.Unlock()

	if IsModelDownloaded(m.root, model) {
		m.set(StatusDone, 1.0, nil, model.ID, "")
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
		return m.State(), nil
	}

	taskID := fmt.Sprintf("%d", time.Now().UnixNano())
	m.set(StatusDownloading, 0.0, nil, model.ID, taskID)

	go func() {
		errMsg := m.runDownload(model, taskID)
		if errMsg != nil {
			m.set(StatusFailed, 0.0, errMsg, model.ID, taskID)
		} else {
			m.set(StatusDone, 1.0, nil, model.ID, taskID)
		}
		m.mu.Lock()
		m.busy = false
		m.mu.Unlock()
	}()
	return m.State(), nil
}

// runDownload ensures the runtime library, downloads the model, then warms
// the session (ports the pip-deps step + download_model_raced + the LOADING
// TextEmbedding warm-up). Runtime bytes account for the first 20% of the
// progress bar; the model download fills the rest.
func (m *Manager) runDownload(model Model, taskID string) *string {
	ctx := context.Background()

	// Stage 1: runtime library (no-op when already present).
	if !DepsAvailable(m.root) {
		dlRuntime := DownloadRuntime
		if m.DownloadRuntimeFn != nil {
			dlRuntime = m.DownloadRuntimeFn
		}
		if err := dlRuntime(ctx, m.root, func(done, _ int64) {
			// The archive is tens of MB; a coarse 0..0.2 ramp suffices.
			progress := float64(done) / (30 * 1024 * 1024)
			m.setProgress(progress*0.2, taskID)
		}); err != nil {
			e := fmt.Sprintf("downloading onnxruntime: %v", err)
			return &e
		}
	}
	m.setProgress(0.2, taskID)

	// Stage 2: model weights.
	expected := int64(model.SizeGB * (1 << 30))
	dlModel := DownloadModel
	if m.DownloadModelFn != nil {
		dlModel = m.DownloadModelFn
	}
	if _, err := dlModel(ctx, m.root, model.ID, func(done, _ int64) {
		if expected <= 0 {
			return
		}
		ratio := float64(done) / float64(expected)
		if ratio > 1 {
			ratio = 1
		}
		m.setProgress(0.2+ratio*0.8, taskID)
	}); err != nil {
		e := err.Error()
		return &e
	}

	// Stage 3: LOADING — warm up the session so the first real embed is fast.
	// Skipped when the runtime library is missing (Python: ImportError →
	// pass); a real load failure still fails the download.
	m.mu.Lock()
	m.state.Status = StatusLoading
	m.mu.Unlock()
	CloseSession(model.ID)
	if DepsAvailable(m.root) {
		if _, err := EmbedTexts(m.root, model.ID, []string{"warmup"}); err != nil {
			e := fmt.Sprintf("model downloaded but failed to load: %v", err)
			return &e
		}
	}
	return nil
}

func (m *Manager) setProgress(progress float64, taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status != StatusDownloading && m.state.Status != StatusLoading {
		return
	}
	if progress <= m.state.Progress {
		return
	}
	m.state.Progress = progress
	m.state.TaskID = taskID
}

// ErrDepsUnavailable is the enable-time error when the runtime library is
// missing (mirrors Python's 503 "local-embedding deps unavailable").
type ErrDepsUnavailable struct{ Detail string }

func (e *ErrDepsUnavailable) Error() string {
	return "local-embedding deps unavailable: " + e.Detail
}

// EnsureEnable checks everything enabling the service requires, mirroring the
// Python order: deps must be available (else ErrDepsUnavailable → 503) and
// weights must be on disk (else an error → 409).
func EnsureEnable(root string, cfg ServiceConfig) error {
	model, ok := Find(cfg.Model)
	if !ok {
		return fmt.Errorf("model not in ONNX catalog: %s", cfg.Model)
	}
	if model.Tokenizer != TokenizerWordPiece {
		return &ErrDepsUnavailable{Detail: fmt.Sprintf(
			"model %s uses the %q tokenizer, which is not supported in the Go build", model.ID, model.Tokenizer)}
	}
	if !DepsAvailable(root) {
		return &ErrDepsUnavailable{Detail: fmt.Sprintf(
			"the ONNX runtime library is missing; download any model from the local models panel to fetch it (expected at %s)",
			RuntimeLibPath(root))}
	}
	if !IsModelDownloaded(root, model) {
		return fmt.Errorf("ONNX embedding model %s is not downloaded yet", model.ID)
	}
	return nil
}
