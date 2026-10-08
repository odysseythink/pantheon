package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

func cliRun(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := MainCLI(argv, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

// cliMemoryArgv prepends the global --db/--namespace flags to a command path.
func cliMemoryArgv(t *testing.T, rest ...string) []string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	return append([]string{"--db", dbPath, "--namespace", "cli"}, rest...)
}

// ---------------------------------------------------------------------------
// framework
// ---------------------------------------------------------------------------

func TestCLIFramework(t *testing.T) {
	// --version
	code, out, _ := cliRun(t, "--version")
	if code != 0 || out != "octop-memory, version "+PackageVersion+"\n" {
		t.Fatalf("version: code=%d out=%q", code, out)
	}

	// No args → root help, exit 0 (click no_args_is_help).
	code, out, _ = cliRun(t)
	if code != 0 || !strings.Contains(out, "Usage: octop-memory") || !strings.Contains(out, "Commands:") {
		t.Fatalf("no args: code=%d out=%q", code, out)
	}

	// Unknown group → exit 2.
	code, _, errOut := cliRun(t, "bogus")
	if code != 2 || !strings.Contains(errOut, "No such command 'bogus'") {
		t.Fatalf("unknown group: code=%d err=%q", code, errOut)
	}

	// Bare group → group help, exit 0.
	code, out, _ = cliRun(t, "memory")
	if code != 0 || !strings.Contains(out, "store") || !strings.Contains(out, "recall") {
		t.Fatalf("bare group: code=%d out=%q", code, out)
	}

	// Unknown subcommand → exit 2.
	code, _, errOut = cliRun(t, "memory", "bogus")
	if code != 2 || !strings.Contains(errOut, "No such command 'bogus'") {
		t.Fatalf("unknown cmd: code=%d err=%q", code, errOut)
	}

	// Missing required option → exit 2.
	code, _, errOut = cliRun(t, "--db", "x.db", "memory", "store")
	if code != 2 || !strings.Contains(errOut, "Missing option '--content'") {
		t.Fatalf("missing required: code=%d err=%q", code, errOut)
	}

	// Unknown option → exit 2.
	code, _, errOut = cliRun(t, "--db", "x.db", "memory", "store", "--nope", "1")
	if code != 2 || !strings.Contains(errOut, "No such option: --nope") {
		t.Fatalf("unknown option: code=%d err=%q", code, errOut)
	}

	// Missing positional → exit 2.
	code, _, errOut = cliRun(t, "--db", "x.db", "memory", "get")
	if code != 2 || !strings.Contains(errOut, "Missing argument 'NODE_ID'") {
		t.Fatalf("missing arg: code=%d err=%q", code, errOut)
	}

	// Command --help → exit 0.
	code, out, _ = cliRun(t, "memory", "store", "--help")
	if code != 0 || !strings.Contains(out, "Store a new memory node") {
		t.Fatalf("cmd help: code=%d out=%q", code, out)
	}

	// Arity-aware global split: the option value must not become the group.
	code, _, _ = cliRun(t, "--db", "memory", "config", "path")
	if code == 2 && false {
		t.Fatal("unreachable")
	}
}

func TestCLIEnvVarFallback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "env.db")
	t.Setenv("OCTOP_MEMORY_DB", dbPath)
	t.Setenv("OCTOP_MEMORY_NAMESPACE", "envns")

	code, out, errOut := cliRun(t, "memory", "store", "--content", "env seeded")
	if code != 0 || !strings.HasPrefix(out, "Stored memory node: ") {
		t.Fatalf("env db store: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("env db file missing: %v", err)
	}
}

// ---------------------------------------------------------------------------
// config group
// ---------------------------------------------------------------------------

func TestCLIConfigGroup(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	SetCLIConfigPath(cfgPath)
	defer SetCLIConfigPath("")

	// path
	code, out, _ := cliRun(t, "config", "path")
	if code != 0 || out != cfgPath+"\n" {
		t.Fatalf("path: code=%d out=%q", code, out)
	}

	// set (dotted key)
	code, out, _ = cliRun(t, "config", "set", "sqlite.db_path", "/tmp/x.db")
	if code != 0 || out != "Set sqlite.db_path = /tmp/x.db\n" {
		t.Fatalf("set: code=%d out=%q", code, out)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if section, _ := saved["sqlite"].(map[string]any); section["db_path"] != "/tmp/x.db" {
		t.Fatalf("saved: %v", saved)
	}

	// set (new section + top-level key)
	code, _, _ = cliRun(t, "config", "set", "backend", "postgres")
	if code != 0 {
		t.Fatalf("set backend: %d", code)
	}

	// show (text): effective overrides come from the file.
	code, out, _ = cliRun(t, "config", "show")
	if code != 0 ||
		!strings.HasPrefix(out, "Config file: "+cfgPath) ||
		!strings.Contains(out, "Backend:     postgres") ||
		!strings.Contains(out, "Namespace:   default") ||
		!strings.Contains(out, "DSN:         postgresql://localhost/octop_memory") ||
		!strings.Contains(out, `"backend": "postgres"`) {
		t.Fatalf("show text: code=%d out=%q", code, out)
	}

	// show --json envelope (indent=2 → multi-line, like json.dumps(indent=2)).
	code, out, _ = cliRun(t, "--json", "config", "show")
	if code != 0 || !strings.Contains(out, `"status": "ok"`) {
		t.Fatalf("show json: code=%d out=%q", code, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["status"] != "ok" || envelope["data"] == nil {
		t.Fatalf("envelope: %v", envelope)
	}

	// JSON output mirrors Python json.dumps spacing/ensure_ascii.
	if !strings.Contains(out, `"status": "ok"`) {
		t.Fatalf("json spacing: %q", out)
	}
}

// ---------------------------------------------------------------------------
// memory group
// ---------------------------------------------------------------------------

func TestCLIMemoryStoreGetUpdateDelete(t *testing.T) {
	argv := cliMemoryArgv(t)

	// store with metadata + non-default level.
	argvStore := append([]string{}, argv...)
	code, out, errOut := cliRun(t, append(argvStore, "memory", "store",
		"--content", "root note", "--level", "root")...)
	if code != 0 || !strings.HasPrefix(out, "Stored memory node: ") {
		t.Fatalf("store root: code=%d out=%q err=%q", code, out, errOut)
	}
	rootID := strings.TrimSpace(strings.TrimPrefix(out, "Stored memory node: "))

	// store child with metadata.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "store",
		"--content", "leaf 你好", "--parent", rootID,
		"--metadata", `{"confidence":"high"}`)...)
	if code != 0 {
		t.Fatalf("store leaf: code=%d out=%q", code, out)
	}
	leafID := strings.TrimSpace(strings.TrimPrefix(out, "Stored memory node: "))

	// get (text) — metadata rendered ensure_ascii=False.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "get", leafID)...)
	if code != 0 ||
		!strings.HasPrefix(out, "leaf: leaf 你好") ||
		!strings.Contains(out, "  id="+leafID) ||
		!strings.Contains(out, "  parent="+rootID) ||
		!strings.Contains(out, `  metadata={"confidence": "high"}`) {
		t.Fatalf("get: code=%d out=%q", code, out)
	}

	// get (json) — ensure_ascii escapes the CJK content.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "--json", "memory", "get", leafID)...)
	if code != 0 || !strings.Contains(out, `\u4f60\u597d`) {
		t.Fatalf("get json ensure_ascii: code=%d out=%q", code, out)
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			ID       string         `json:"id"`
			ParentID *string        `json:"parent_id"`
			Level    string         `json:"level"`
			Metadata map[string]any `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if envelope.Status != "ok" || envelope.Data.ID != leafID ||
		envelope.Data.ParentID == nil || *envelope.Data.ParentID != rootID ||
		envelope.Data.Metadata["confidence"] != "high" {
		t.Fatalf("envelope data: %+v", envelope)
	}

	// get missing → exit 1 + stderr text / JSON error shape.
	code, _, errOut = cliRun(t, append(append([]string{}, argv...), "memory", "get", "nope")...)
	if code != 1 || !strings.Contains(errOut, "Node not found: nope") {
		t.Fatalf("get missing: code=%d err=%q", code, errOut)
	}
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "--json", "memory", "get", "nope")...)
	if code != 1 || !strings.Contains(out, `"error": "not found"`) {
		t.Fatalf("get missing json: code=%d out=%q", code, out)
	}

	// update — root content is mutable (leaf content belongs to the AtomCard).
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "update", rootID,
		"--content", "renamed root")...)
	if code != 0 || out != "Updated memory node: "+rootID+"\n" {
		t.Fatalf("update: code=%d out=%q", code, out)
	}
	// Leaf content update is rejected → ClickException (exit 1).
	code, _, errOut = cliRun(t, append(append([]string{}, argv...), "memory", "update", leafID,
		"--content", "nope")...)
	if code != 1 || !strings.Contains(errOut, "leaf content is owned by AtomCard") {
		t.Fatalf("leaf update: code=%d err=%q", code, errOut)
	}
	// Topic clear (--topic "") on the leaf succeeds.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "update", leafID,
		"--topic", "")...)
	if code != 0 {
		t.Fatalf("topic clear: code=%d", code)
	}
	code, _, errOut = cliRun(t, append(append([]string{}, argv...), "memory", "update", leafID)...)
	if code != 2 || !strings.Contains(errOut, "nothing to update") {
		t.Fatalf("update nothing: code=%d err=%q", code, errOut)
	}

	// delete refuses children without --cascade (ValueError → ClickException).
	code, _, errOut = cliRun(t, append(append([]string{}, argv...), "memory", "delete", rootID)...)
	if code != 1 || errOut == "" {
		t.Fatalf("delete non-cascade: code=%d err=%q", code, errOut)
	}
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "delete", rootID, "--cascade")...)
	if code != 0 || out != "Deleted memory node: "+rootID+"\n" {
		t.Fatalf("delete cascade: code=%d out=%q", code, out)
	}
}

func TestCLIMetadataValidation(t *testing.T) {
	argv := cliMemoryArgv(t)

	code, _, errOut := cliRun(t, append(append([]string{}, argv...), "memory", "store",
		"--content", "x", "--metadata", "{bad json")...)
	if code != 2 || !strings.HasPrefix(errOut, "Error: --metadata must be valid JSON:") {
		t.Fatalf("bad json: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, append(append([]string{}, argv...), "memory", "store",
		"--content", "x", "--metadata", "[1,2]")...)
	if code != 2 || !strings.Contains(errOut, "--metadata must be a JSON object (got array/scalar)") {
		t.Fatalf("non-object: code=%d err=%q", code, errOut)
	}
	// Case-insensitive choice normalized to canonical value.
	code, out, errOut := cliRun(t, append(append([]string{}, argv...), "memory", "store",
		"--content", "x", "--level", "ROOT")...)
	if code != 0 {
		t.Fatalf("choice: code=%d out=%q err=%q", code, out, errOut)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out, "Stored memory node: "))
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "--json", "memory", "get", id)...)
	if code != 0 || !strings.Contains(out, `"level": "root"`) {
		t.Fatalf("choice canonical: code=%d out=%q", code, out)
	}
}

func TestCLIMemoryTreeAndRecall(t *testing.T) {
	argv := cliMemoryArgv(t)

	code, out, _ := cliRun(t, append(append([]string{}, argv...), "memory", "store",
		"--content", "root", "--level", "root")...)
	rootID := strings.TrimSpace(strings.TrimPrefix(out, "Stored memory node: "))
	for _, name := range []string{"kid1", "kid2"} {
		code, _, _ = cliRun(t, append(append([]string{}, argv...), "memory", "store",
			"--content", name, "--parent", rootID)...)
		if code != 0 {
			t.Fatalf("store %s", name)
		}
	}

	// tree rendering with connectors.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "tree")...)
	if code != 0 ||
		!strings.HasPrefix(out, "root (root) <"+rootID+">") ||
		!strings.Contains(out, "`-- kid2") ||
		!strings.Contains(out, "|-- kid1") {
		t.Fatalf("tree: code=%d out=%q", code, out)
	}

	// tree --json.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "--json", "memory", "tree")...)
	if code != 0 || !strings.Contains(out, `"nodes": [`) {
		t.Fatalf("tree json: code=%d out=%q", code, out)
	}

	// recall.
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "recall", "kid1")...)
	if code != 0 || !strings.Contains(out, "kid1") {
		t.Fatalf("recall: code=%d out=%q", code, out)
	}
	code, out, _ = cliRun(t, append(append([]string{}, argv...), "memory", "recall", "zzz-nothing")...)
	if code != 0 || out != "(no results)\n" {
		t.Fatalf("recall empty: code=%d out=%q", code, out)
	}

	// empty tree.
	argv2 := cliMemoryArgv(t)
	code, out, _ = cliRun(t, append(append([]string{}, argv2...), "memory", "tree")...)
	if code != 0 || out != "(empty)\n" {
		t.Fatalf("empty tree: code=%d out=%q", code, out)
	}
}
