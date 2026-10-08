// Package memory — adapters/bridge port (OpenClaw/Hermes memory bridge).
//
// The octopmemory ↔ OpenClaw plugin bridge: the TS plugin shell spawns
// the bridge as a subprocess and talks to it over stdin/stdout using a
// tiny JSON-RPC 2.0 dialect.
//
// Design (from the Python package docstring):
//
//   - D51-C (path projection): SQLite-backed atoms / pages / raw events
//     project onto a virtual file tree (atom/<id>.md etc.) so the host's
//     file-shaped memory_get(path, from, lines) API works; real files
//     (MEMORY.md, memory/YYYY-MM-DD.md) are ALSO indexed via host_files.
//   - D52-C (auto-capture): handlers.capture is the entry point for the
//     TS agent_end hook.
//
// The bridge is intentionally minimal: no auth, no encryption — it runs
// as a child process of OpenClaw on the same machine as the user. It
// does not expose a TCP/HTTP or remote multi-tenant transport.
package memory

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// PackageVersion mirrors octop_memory.__version__ (kept in sync with the
// project release notes).
const PackageVersion = "0.0.9"

// ProtocolVersion is the wire protocol version (PROTOCOL_VERSION).
// Bumped only on breaking changes to the JSON-RPC method names or
// payload schemas. The TS client checks this on handshake and refuses
// to start on mismatch.
const ProtocolVersion = "1.0"

// ---------------------------------------------------------------------------
// handlers.py — JSON-RPC error codes and the Bridge dispatcher
// ---------------------------------------------------------------------------

// JSON-RPC error codes. Protocol errors use the reserved JSON-RPC
// range; application errors live in -32000..-32099.
const (
	ErrParse                = -32700
	ErrInvalidRequest       = -32600
	ErrMethodNotFound       = -32601
	ErrInvalidParams        = -32602
	ErrInternal             = -32603
	ErrPathNotFound         = -32010
	ErrPathInvalid          = -32011
	ErrHostFileUnavailable  = -32012
)

// Bridge is the JSON-RPC adapter over MemoryRuntime.
type Bridge struct {
	memory   *Memory
	runtime  *MemoryRuntime
	dispatch map[string]BridgeHandlerFunc
}

// BridgeHandlerFunc processes one JSON-RPC method's params.
type BridgeHandlerFunc func(ctx context.Context, params map[string]any) (any, error)

// BridgeOptions customizes a Bridge (nil-able fields).
type BridgeOptions struct {
	HostFiles *HostFilesIndex
	Config    *MemoryRuntimeConfig
	LLM       LLMClient
}

// NewBridge wires the dispatch table: the 8 runtime methods plus the 25
// dashboard_data methods.
func NewBridge(m *Memory, opts *BridgeOptions) (*Bridge, error) {
	if opts == nil {
		opts = &BridgeOptions{}
	}
	var runtimeOpts []MemoryRuntimeOption
	if opts.HostFiles != nil {
		runtimeOpts = append(runtimeOpts, WithRuntimeHostFiles(opts.HostFiles))
	}
	var cfg any
	if opts.Config != nil {
		cfg = opts.Config
	}
	if opts.LLM != nil {
		runtimeOpts = append(runtimeOpts, WithRuntimeLLM(opts.LLM))
	}
	runtime := NewMemoryRuntime(m, cfg, runtimeOpts...)
	b := &Bridge{memory: m, runtime: runtime, dispatch: map[string]BridgeHandlerFunc{}}

	// Stats returns only a map (no error) — wrap it into the uniform shape.
	b.dispatch["stats"] = func(ctx context.Context, _ map[string]any) (any, error) {
		return runtime.Stats(ctx), nil
	}
	b.dispatch["handshake"] = func(ctx context.Context, params map[string]any) (any, error) {
		return b.handshake(ctx, params)
	}
	b.dispatch["reindex"] = func(ctx context.Context, _ map[string]any) (any, error) {
		return runtime.Reindex(ctx)
	}
	for name, fn := range map[string]func(ctx context.Context, params map[string]any) (map[string]any, error){
		"memory_search": runtime.MemorySearch,
		"memory_get":    runtime.MemoryGet,
		"capture":       runtime.Capture,
		"extract":       runtime.Extract,
		"promote":       runtime.Promote,
	} {
		fn := fn
		b.dispatch[name] = func(ctx context.Context, params map[string]any) (any, error) {
			return fn(ctx, params)
		}
	}
	for name, fn := range BuildDashboardDispatch(m) {
		b.dispatch[name] = BridgeHandlerFunc(fn)
	}
	return b, nil
}

func (b *Bridge) handshake(_ context.Context, params map[string]any) (map[string]any, error) {
	clientVersion := params["client_version"]
	return map[string]any{
		"protocol_version": ProtocolVersion,
		"client_version":   clientVersion,
		"server":           "octop-memory.bridge",
		"namespace":        b.memory.Namespace(),
	}, nil
}

// Handle dispatches a single JSON-RPC request to a response dict.
func (b *Bridge) Handle(ctx context.Context, request map[string]any) map[string]any {
	rpcID := request["id"]
	method, _ := request["method"].(string)
	params, _ := request["params"].(map[string]any)
	if request["params"] == nil {
		params = map[string]any{}
	}

	if request["method"] == nil {
		return bridgeError(rpcID, ErrInvalidRequest, "request missing 'method' string")
	}
	if _, ok := request["method"].(string); !ok {
		return bridgeError(rpcID, ErrInvalidRequest, "request missing 'method' string")
	}
	if paramsAny, present := request["params"]; present && paramsAny != nil {
		if _, ok := paramsAny.(map[string]any); !ok {
			return bridgeError(rpcID, ErrInvalidParams, "'params' must be an object")
		}
	}

	handler, ok := b.dispatch[method]
	if !ok {
		return bridgeError(rpcID, ErrMethodNotFound, fmt.Sprintf("unknown method %s", pyReprScalar(method)))
	}

	result, err := handler(ctx, params)
	if err != nil {
		switch e := err.(type) {
		case *NotFoundError:
			return bridgeError(rpcID, ErrPathNotFound, e.msg)
		case *ddNotFoundError:
			return bridgeError(rpcID, ErrPathNotFound, e.msg)
		case *HostFileUnavailableError:
			return bridgeError(rpcID, ErrHostFileUnavailable, e.msg)
		case *PathError:
			return bridgeError(rpcID, ErrPathInvalid, e.msg)
		case *paramError:
			return bridgeError(rpcID, ErrInvalidParams, e.msg)
		default:
			return bridgeError(rpcID, ErrInternal, fmt.Sprintf("%T: %v", err, err))
		}
	}
	return map[string]any{"jsonrpc": "2.0", "id": rpcID, "result": result}
}

// bridgeError mirrors handlers._error.
func bridgeError(rpcID any, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"error":   map[string]any{"code": code, "message": message},
	}
}

// ---------------------------------------------------------------------------
// server.py — stdio JSON-RPC server loop
// ---------------------------------------------------------------------------

// ServeBridge reads line-framed JSON-RPC requests until EOF, writing one
// JSON response per line. Exposed as a top-level function so tests can
// drive it with in-memory pipes (mirrors bridge.server.serve).
func ServeBridge(bridge *Bridge, src io.Reader, sink io.Writer) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		response := HandleOneBridgeLine(bridge, line)
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err := sink.Write(append(encoded, '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// HandleOneBridgeLine parses one line into a JSON-RPC response dict.
// Parse errors return a -32700 envelope with id: null per spec;
// downstream errors are produced by Bridge.Handle.
func HandleOneBridgeLine(bridge *Bridge, line string) map[string]any {
	var request any
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		return bridgeError(nil, ErrParse, fmt.Sprintf("could not parse JSON: %s", err.Error()))
	}
	reqObj, ok := request.(map[string]any)
	if !ok {
		return bridgeError(nil, ErrParse, "request must be a JSON object")
	}
	return bridge.Handle(context.Background(), reqObj)
}

// ---------------------------------------------------------------------------
// server.py — CLI entry (octopmemory-bridge)
// ---------------------------------------------------------------------------

// BridgeCLIFlags carries the parsed octopmemory-bridge arguments.
type BridgeCLIFlags struct {
	Namespace             string
	DBPath                string
	Backend               string // "sqlite" (postgres unsupported in this port)
	DSNEnv                string
	HostFilesRoot         string
	HostFilesAllow        []string
	HostFilesPollInterval float64
	ConfigJSON            string
	Probe                 bool
}

// DefaultBridgeDBPath builds the default SQLite path for a namespace:
// $OCTOPMEMORY_HOME/<ns>/memory.sqlite else ~/.octopmemory/<ns>/memory.sqlite.
func DefaultBridgeDBPath(namespace string) string {
	home := os.Getenv("OCTOPMEMORY_HOME")
	base := home
	if base == "" {
		home2, err := os.UserHomeDir()
		if err != nil {
			base = "."
		} else {
			base = filepath.Join(home2, ".octopmemory")
		}
	}
	return filepath.Join(base, namespace, "memory.sqlite")
}

// RunBridgeProbe prints a one-line JSON environment report (protocol /
// version / sqlite / fts5) and returns the process exit code. Exits
// non-zero when FTS5 is unavailable.
func RunBridgeProbe(w io.Writer) int {
	fts5, sqliteVersion, fts5Err := probeFTS5()
	report := map[string]any{
		"ok":                   fts5,
		"protocol_version":     ProtocolVersion,
		"octop_memory_version": PackageVersion,
		"go_version":           runtimeGoVersion(),
		"sqlite_version":       sqliteVersion,
		"fts5":                 fts5,
		"fts5_error":           nil,
	}
	if fts5Err != "" {
		report["fts5_error"] = fts5Err
	}
	encoded, _ := json.Marshal(report)
	fmt.Fprintln(w, string(encoded))
	if fts5 {
		return 0
	}
	return 1
}

// runtimeGoVersion reports the Go toolchain version (probe parity with
// the Python report's python_version field).
func runtimeGoVersion() string {
	return goruntime.Version()
}

// probeFTS5 verifies FTS5 is available in the linked SQLite and returns
// the engine version string.
func probeFTS5() (ok bool, sqliteVersion, errMsg string) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return false, "", err.Error()
	}
	defer db.Close()
	_ = db.QueryRow("SELECT sqlite_version()").Scan(&sqliteVersion)
	if _, err := db.Exec("CREATE VIRTUAL TABLE _probe_fts USING fts5(x)"); err != nil {
		return false, sqliteVersion, err.Error()
	}
	return true, sqliteVersion, ""
}

// RunBridgeCLI is the octopmemory-bridge main: probe short-circuit,
// Memory construction, host_files setup, then the stdio serve loop.
func RunBridgeCLI(ctx context.Context, flags BridgeCLIFlags, src io.Reader, sink io.Writer, logs io.Writer) (int, error) {
	if flags.Probe {
		return RunBridgeProbe(sink), nil
	}
	if flags.Namespace == "" {
		return 2, fmt.Errorf("--namespace is required")
	}

	dbPath := flags.DBPath
	if dbPath == "" {
		dbPath = DefaultBridgeDBPath(flags.Namespace)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return 2, fmt.Errorf("portable: create db dir: %w", err)
	}
	// This Go port ships the SQLite backend only; the postgres backend
	// selection is rejected rather than silently mis-serving.
	if flags.Backend == "postgres" {
		return 2, fmt.Errorf("--backend=postgres is not supported by the Go bridge port (use sqlite)")
	}

	memory, err := NewMemory(flags.Namespace, WithMemoryDBPath(dbPath))
	if err != nil {
		return 2, fmt.Errorf("open memory: %w", err)
	}

	var hostFiles *HostFilesIndex
	if flags.HostFilesRoot != "" {
		root, err := filepath.Abs(expandHomePath(flags.HostFilesRoot))
		if err != nil {
			return 2, err
		}
		includeGlobs := splitCSVArgs(flags.HostFilesAllow)
		if len(includeGlobs) == 0 {
			includeGlobs = append([]string{}, DefaultHostFileGlobs...)
		}
		hostFiles, err = NewHostFilesIndex(dbPath, flags.Namespace, includeGlobs)
		if err != nil {
			return 2, err
		}
		report, err := hostFiles.ScanOnce(ctx, root)
		if err != nil {
			return 2, err
		}
		_, _ = fmt.Fprintf(logs, "host_files initial scan: scanned=%d indexed=%d removed=%d\n",
			report.Scanned, report.Indexed, report.Removed)
	}

	config, err := parseBridgeConfig(flags.ConfigJSON)
	if err != nil {
		return 2, err
	}

	bridge, err := NewBridge(memory, &BridgeOptions{HostFiles: hostFiles, Config: config})
	if err != nil {
		return 2, err
	}
	_, _ = fmt.Fprintf(logs, "octopmemory bridge started (namespace=%s backend=sqlite db=%s host_files=%s)\n",
		flags.Namespace, dbPath, orDefault(flags.HostFilesRoot, "off"))

	if err := ServeBridge(bridge, src, sink); err != nil {
		return 1, err
	}
	return 0, nil
}

// parseBridgeConfig decodes --config-json into a MemoryRuntimeConfig
// (invalid JSON / non-object → error, like the Python SystemExit).
func parseBridgeConfig(raw string) (*MemoryRuntimeConfig, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var loaded map[string]any
	if err := json.Unmarshal([]byte(raw), &loaded); err != nil {
		return nil, fmt.Errorf("--config-json is not valid JSON: %v", err)
	}
	cfg := &MemoryRuntimeConfig{}
	profile, _ := loaded["profile"].(string)
	if profile != "" {
		cfg.Profile = &profile
	}
	mode, _ := loaded["mode"].(string)
	if mode != "" {
		cfg.Mode = &mode
	}
	cfg.Recall = bridgeDictValue(loaded["recall"])
	cfg.Capture = bridgeDictValue(loaded["capture"])
	cfg.Privacy = bridgeDictValue(loaded["privacy"])
	cfg.LLM = bridgeDictValue(loaded["llm"])
	cfg.Extraction = bridgeDictValue(loaded["extraction"])
	return cfg, nil
}

func bridgeDictValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func splitCSVArgs(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
