package memory

// CLI tests for the openclaw command group (openclaw_cmd.py) and the
// portable command group (portable_cmd.py).
//
// Both groups resolve paths under $HOME, so the tests redirect HOME /
// USERPROFILE to a fresh t.TempDir() via t.Setenv and build fixtures
// beneath it.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliRedirectHome points expandHomePath at a fresh temp dir and returns it.
func cliRedirectHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestCLIOpenclawSetup(t *testing.T) {
	home := cliRedirectHome(t)
	cfgPath := filepath.Join(home, "openclaw.json")

	// dry-run prints the resulting JSON and does NOT write the file.
	code, out, errOut := cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath, "--dry-run")
	if code != 0 {
		t.Fatalf("setup dry-run: code=%d out=%q err=%q", code, out, errOut)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("setup dry-run output is not JSON: %v\n%s", err, out)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("setup dry-run must not write the config (stat err=%v)", err)
	}
	plugins := cfg["plugins"].(map[string]any)
	if plugins["slots"].(map[string]any)["memory"] != "octopmemory" {
		t.Fatalf("slots.memory = %v", plugins["slots"].(map[string]any)["memory"])
	}
	entry := plugins["entries"].(map[string]any)["octopmemory"].(map[string]any)
	if entry["enabled"] != true {
		t.Fatalf("entry.enabled = %v", entry["enabled"])
	}
	if entry["hooks"].(map[string]any)["allowConversationAccess"] != true {
		t.Fatalf("entry hooks = %v", entry["hooks"])
	}
	pc := entry["config"].(map[string]any)
	if pc["namespace"] != "openclaw__default" {
		t.Fatalf("namespace = %v", pc["namespace"])
	}
	wantDB := filepath.Join(home, "octopmemory", "openclaw__default", "memory.sqlite")
	if pc["db_path"] != wantDB {
		t.Fatalf("db_path = %v want %v", pc["db_path"], wantDB)
	}
	recall := pc["recall"].(map[string]any)
	if recall["mode"] != "tool_hint" || recall["default_max_results"] != float64(5) {
		t.Fatalf("balanced recall = %v", recall)
	}
	if pc["profile"] != "balanced" {
		t.Fatalf("profile = %v", pc["profile"])
	}
	if pc["capture"].(map[string]any)["agent_end_hook"] != true {
		t.Fatalf("agent_end_hook = %v", pc["capture"].(map[string]any)["agent_end_hook"])
	}
	// balanced keeps the host_files watcher on → host_files_root written
	// under the redirected home.
	hfr, _ := pc["host_files_root"].(string)
	if !strings.HasPrefix(hfr, home) {
		t.Fatalf("host_files_root = %v, want under %s", pc["host_files_root"], home)
	}
	if got := pc["host_files_allow"].([]any); len(got) != 2 || got[0] != "topics/*.md" {
		t.Fatalf("host_files_allow = %v", got)
	}
	if _, has := pc["bridge"].(map[string]any)["python"]; !has {
		t.Fatalf("bridge = %v, want a python fallback", pc["bridge"])
	}

	// low_latency profile: watcher off → no host_files_root; recall overrides.
	code, out, _ = cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath,
		"--profile", "low_latency", "--dry-run")
	if code != 0 {
		t.Fatalf("low_latency setup: code=%d out=%q", code, out)
	}
	var cfg2 map[string]any
	_ = json.Unmarshal([]byte(out), &cfg2)
	pc2 := cfg2["plugins"].(map[string]any)["entries"].(map[string]any)["octopmemory"].(map[string]any)["config"].(map[string]any)
	if pc2["capture"].(map[string]any)["host_files_watcher"] != false {
		t.Fatalf("low_latency watcher = %v", pc2["capture"].(map[string]any)["host_files_watcher"])
	}
	if _, has := pc2["host_files_root"]; has {
		t.Fatalf("low_latency must not write host_files_root: %v", pc2["host_files_root"])
	}
	if pc2["recall"].(map[string]any)["default_max_results"] != float64(3) {
		t.Fatalf("low_latency max_results = %v", pc2["recall"])
	}

	// --namespace / --db-path override; recall-mode switch.
	code, out, _ = cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath,
		"--namespace", "myns", "--db-path", filepath.Join(home, "x.db"),
		"--recall-mode", "hybrid", "--no-agent-end-hook", "--dry-run")
	if code != 0 {
		t.Fatalf("ns setup: code=%d out=%q", code, out)
	}
	var cfg3 map[string]any
	_ = json.Unmarshal([]byte(out), &cfg3)
	pc3 := cfg3["plugins"].(map[string]any)["entries"].(map[string]any)["octopmemory"].(map[string]any)["config"].(map[string]any)
	if pc3["namespace"] != "myns" || pc3["db_path"] != filepath.Join(home, "x.db") {
		t.Fatalf("ns/db = %v / %v", pc3["namespace"], pc3["db_path"])
	}
	if pc3["recall"].(map[string]any)["mode"] != "hybrid" {
		t.Fatalf("recall mode = %v", pc3["recall"])
	}
	if pc3["capture"].(map[string]any)["agent_end_hook"] != false {
		t.Fatalf("agent_end_hook = %v", pc3["capture"].(map[string]any)["agent_end_hook"])
	}

	// Real run writes the file, prints the banner, and is idempotent.
	code, out, _ = cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath)
	if code != 0 ||
		!strings.HasPrefix(out, "wrote "+cfgPath+"\n") ||
		!strings.Contains(out, "  plugins.slots.memory = octopmemory\n") ||
		!strings.Contains(out, "  plugins.entries.octopmemory.hooks.allowConversationAccess = true\n") ||
		!strings.Contains(out, "  plugins.entries.octopmemory.config.namespace = openclaw__default\n") ||
		!strings.Contains(out, "Next steps:\n") ||
		!strings.Contains(out, "Verify: octop-memory openclaw doctor --openclaw-config "+cfgPath+"\n") {
		t.Fatalf("setup real run: code=%d out=%q", code, out)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("setup must write the config: %v", err)
	}
	code, out, _ = cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath, "--dry-run")
	if code != 0 || !strings.Contains(out, `"profile": "balanced"`) {
		t.Fatalf("second setup (idempotent re-read): code=%d out=%q", code, out)
	}

	// --openclaw-config pointing at a directory → click.Path(dir_okay=False).
	code, _, errOut = cliRun(t, "openclaw", "setup", "--openclaw-config", home)
	if code != 2 || !strings.Contains(errOut, "Invalid value for '--openclaw-config': '"+home+"' is a directory.") {
		t.Fatalf("dir config: code=%d err=%q", code, errOut)
	}

	// Bad profile → choice error.
	code, _, errOut = cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath, "--profile", "bogus")
	if code != 2 || !strings.Contains(errOut, "Invalid value for '--profile'") {
		t.Fatalf("bad profile: code=%d err=%q", code, errOut)
	}
}

func TestCLIOpenclawDoctor(t *testing.T) {
	home := cliRedirectHome(t)
	cfgPath := filepath.Join(home, "openclaw.json")

	// No config at all: config missing + plugin dir missing.
	code, out, errOut := cliRun(t, "openclaw", "doctor", "--openclaw-config", cfgPath)
	if code != 1 ||
		!strings.HasPrefix(out, "Checking "+cfgPath+" ...\n") ||
		!strings.Contains(out, "  [FAIL] openclaw config not found: "+cfgPath+"\n") ||
		!strings.Contains(out, "  [FAIL] plugin directory missing: ") ||
		!strings.Contains(out, "  [OK]   octop_memory "+PackageVersion+" importable (bridge protocol "+ProtocolVersion+")\n") ||
		!strings.Contains(out, "with FTS5") ||
		!strings.HasSuffix(out, "❌ 2 check(s) failed.\n") {
		t.Fatalf("doctor no config: code=%d out=%q err=%q", code, out, errOut)
	}

	// After setup: config checks all pass, only the plugin dir fails.
	if code, out, errOut := cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath); code != 0 {
		t.Fatalf("setup: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = cliRun(t, "openclaw", "doctor", "--openclaw-config", cfgPath)
	if code != 1 ||
		!strings.Contains(out, "  [OK]   openclaw config exists ("+cfgPath+")\n") ||
		!strings.Contains(out, "  [OK]   plugins.slots.memory = octopmemory\n") ||
		!strings.Contains(out, "  [OK]   plugins.entries.octopmemory.enabled = true\n") ||
		!strings.Contains(out, "  [OK]   plugins.entries.octopmemory.hooks.allowConversationAccess = true\n") ||
		!strings.Contains(out, "  [FAIL] plugin directory missing: ") ||
		!strings.HasSuffix(out, "❌ 1 check(s) failed.\n") {
		t.Fatalf("doctor after setup: code=%d out=%q", code, out)
	}

	// Unparseable config → parseable check fails, slot checks skipped.
	broken := filepath.Join(home, "broken.json")
	if err := os.WriteFile(broken, []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = cliRun(t, "openclaw", "doctor", "--openclaw-config", broken)
	if code != 1 ||
		!strings.Contains(out, "  [FAIL] openclaw config not parseable: ") ||
		!strings.HasSuffix(out, "❌ 2 check(s) failed.\n") {
		t.Fatalf("doctor broken config: code=%d out=%q", code, out)
	}
}

func TestCLIOpenclawUninstall(t *testing.T) {
	home := cliRedirectHome(t)
	cfgPath := filepath.Join(home, "openclaw.json")

	// Missing config → friendly no-op.
	code, out, _ := cliRun(t, "openclaw", "uninstall", "--openclaw-config", cfgPath, "--yes")
	if code != 0 || out != "nothing to remove: "+cfgPath+" does not exist\n" {
		t.Fatalf("uninstall missing: code=%d out=%q", code, out)
	}

	if code, out, errOut := cliRun(t, "openclaw", "setup", "--openclaw-config", cfgPath); code != 0 {
		t.Fatalf("setup: code=%d out=%q err=%q", code, out, errOut)
	}

	// Without --yes, click.confirm aborts on EOF stdin.
	code, out, errOut := cliRun(t, "openclaw", "uninstall", "--openclaw-config", cfgPath)
	if code != 1 ||
		!strings.HasPrefix(out, "Remove the octopmemory slot binding? (your stored memory data is NOT deleted.) [y/N]: ") ||
		errOut != "Aborted!\n" {
		t.Fatalf("uninstall confirm: code=%d out=%q err=%q", code, out, errOut)
	}

	// --yes removes the slot but keeps the entries block.
	code, out, _ = cliRun(t, "openclaw", "uninstall", "--openclaw-config", cfgPath, "--yes")
	if code != 0 || out != "removed plugins.slots.memory (octopmemory) from "+cfgPath+"\n" {
		t.Fatalf("uninstall yes: code=%d out=%q", code, out)
	}
	raw, _ := os.ReadFile(cfgPath)
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	slots := cfg["plugins"].(map[string]any)["slots"].(map[string]any)
	if _, has := slots["memory"]; has {
		t.Fatalf("slots.memory still present: %v", slots)
	}
	if _, has := cfg["plugins"].(map[string]any)["entries"].(map[string]any)["octopmemory"]; !has {
		t.Fatal("entries block must be preserved by uninstall")
	}

	// Second uninstall → no-op with repr of the missing value.
	code, out, _ = cliRun(t, "openclaw", "uninstall", "--openclaw-config", cfgPath, "--yes")
	if code != 0 || out != "no-op: plugins.slots.memory = None, not 'octopmemory'\n" {
		t.Fatalf("uninstall no-op: code=%d out=%q", code, out)
	}
}

func TestCLIOpenclawPrintConfig(t *testing.T) {
	home := cliRedirectHome(t)

	code, out, errOut := cliRun(t, "openclaw", "print-config")
	if code != 0 {
		t.Fatalf("print-config: code=%d out=%q err=%q", code, out, errOut)
	}
	var sample map[string]any
	if err := json.Unmarshal([]byte(out), &sample); err != nil {
		t.Fatalf("print-config output is not JSON: %v\n%s", err, out)
	}
	entry := sample["plugins"].(map[string]any)["entries"].(map[string]any)["octopmemory"].(map[string]any)
	pc := entry["config"].(map[string]any)
	if sample["plugins"].(map[string]any)["slots"].(map[string]any)["memory"] != "octopmemory" {
		t.Fatal("print-config slots.memory")
	}
	bridge := pc["bridge"].(map[string]any)
	if bridge["python"] != "python3" || bridge["spawn_timeout_ms"] != float64(5000) {
		t.Fatalf("print-config bridge = %v", bridge)
	}
	if pc["namespace"] != "openclaw__default" {
		t.Fatalf("print-config namespace = %v", pc["namespace"])
	}
	if _, has := pc["host_files_root"]; !has {
		t.Fatal("balanced print-config should carry host_files_root")
	}

	// privacy profile flips storage defaults.
	code, out, _ = cliRun(t, "openclaw", "print-config", "--profile", "privacy")
	if code != 0 {
		t.Fatalf("print-config privacy: code=%d out=%q", code, out)
	}
	_ = json.Unmarshal([]byte(out), &sample)
	pc = sample["plugins"].(map[string]any)["entries"].(map[string]any)["octopmemory"].(map[string]any)["config"].(map[string]any)
	if pc["privacy"].(map[string]any)["store_raw_content"] != false {
		t.Fatalf("privacy store_raw_content = %v", pc["privacy"])
	}
	if pc["recall"].(map[string]any)["raw_policy"] != "never" {
		t.Fatalf("privacy raw_policy = %v", pc["recall"])
	}
	if _, has := pc["host_files_root"]; has {
		t.Fatal("privacy turns the watcher off → no host_files_root")
	}
	_ = home
}

// ---------------------------------------------------------------------------
// portable
// ---------------------------------------------------------------------------

// cliPortableSeedHermes builds a hermes-layout source store under the
// redirected HOME and returns the fake home.
func cliPortableSeedHermes(t *testing.T) string {
	t.Helper()
	home := cliRedirectHome(t)
	hermesDir := filepath.Join(home, ".hermes", "octopmemory")
	if err := os.MkdirAll(hermesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewMemory("hermes", WithMemoryDBPath(filepath.Join(hermesDir, "memory.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, context.Background(), m)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestCLIPortableListSources(t *testing.T) {
	home := cliPortableSeedHermes(t)

	// Text mode finds the hermes store.
	code, out, errOut := cliRun(t, "portable", "list-sources")
	if code != 0 ||
		!strings.HasPrefix(out, "Found 1 memory store(s):\n\n") ||
		!strings.Contains(out, "  [1] hermes:hermes\n") ||
		!strings.Contains(out, "      namespace:   hermes\n") ||
		!strings.Contains(out, "      db_path:     "+filepath.Join(home, ".hermes", "octopmemory", "memory.sqlite")+"\n") ||
		!strings.Contains(out, "      counts:      raw_events=3  atoms=1  entities=1  journal=2\n") ||
		!strings.HasSuffix(out, "      schema_ver:  1\n\n") {
		t.Fatalf("list-sources text: code=%d out=%q err=%q", code, out, errOut)
	}

	// JSON mode is an array of source dicts.
	code, out, _ = cliRun(t, "portable", "list-sources", "--json")
	if code != 0 {
		t.Fatalf("list-sources json: code=%d out=%q", code, out)
	}
	var sources []map[string]any
	if err := json.Unmarshal([]byte(out), &sources); err != nil {
		t.Fatalf("list-sources json parse: %v\n%s", err, out)
	}
	if len(sources) != 1 ||
		sources[0]["host_kind"] != "hermes" ||
		sources[0]["namespace"] != "hermes" ||
		sources[0]["raw_event_count"] != float64(3) ||
		sources[0]["agent_name"] != "hermes" {
		t.Fatalf("list-sources json = %v", sources)
	}
}

func TestCLIPortablePackAdoptDoctor(t *testing.T) {
	_ = cliPortableSeedHermes(t)
	pkgPath := filepath.Join(t.TempDir(), "hermes.hmpkg")

	// pack resolves the "hermes" source through list-sources discovery.
	code, out, errOut := cliRun(t, "portable", "pack", "--from", "hermes", "--out", pkgPath)
	if code != 0 ||
		errOut != "  Exporting memory...\n  Packing...\n" ||
		!strings.HasPrefix(out, "packed ") ||
		!strings.Contains(out, " rows from hermes -> "+pkgPath+" (") ||
		!strings.HasSuffix(out, " KB)\n") {
		t.Fatalf("pack: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Stat(pkgPath); err != nil {
		t.Fatalf("pack output file: %v", err)
	}

	// Unknown source → Error + exit 1 (Python ValueError branch).
	code, _, errOut = cliRun(t, "portable", "pack", "--from", "bogus")
	if code != 1 || !strings.HasPrefix(errOut, "Error: source memory store not found: 'bogus'") {
		t.Fatalf("pack bogus: code=%d err=%q", code, errOut)
	}

	// adopt into openclaw:myns → ~/.octopmemory/myns/memory.sqlite.
	code, out, errOut = cliRun(t, "portable", "adopt", pkgPath, "--as", "openclaw:myns")
	if code != 0 ||
		errOut != "  Importing memory...\n" ||
		!strings.HasPrefix(out, "adopted: ") ||
		!strings.Contains(out, " -> myns (") {
		t.Fatalf("adopt: code=%d out=%q err=%q", code, out, errOut)
	}

	// Re-adopting the same package is a no-op (journal idempotency).
	code, out, _ = cliRun(t, "portable", "adopt", pkgPath, "--as", "openclaw:myns")
	if code != 0 || !strings.HasPrefix(out, "already adopted at ") || !strings.HasSuffix(out, ", skipping.\n") {
		t.Fatalf("re-adopt: code=%d out=%q", code, out)
	}

	// dry-run on a fresh namespace only pre-checks.
	code, out, _ = cliRun(t, "portable", "adopt", pkgPath, "--as", "openclaw:dryns", "--dry-run")
	if code != 0 ||
		!strings.HasPrefix(out, "[dry-run] would write approximately ") ||
		!strings.Contains(out, " -> dryns (") {
		t.Fatalf("adopt dry-run: code=%d out=%q", code, out)
	}

	// Missing package argument → click.Path(exists=True).
	code, _, errOut = cliRun(t, "portable", "adopt", filepath.Join(t.TempDir(), "nope.hmpkg"), "--as", "openclaw:x")
	if code != 2 || !strings.Contains(errOut, "Invalid value for 'PKG_FILE': Path '") {
		t.Fatalf("adopt missing pkg: code=%d err=%q", code, errOut)
	}

	// doctor --json against the adopted store: all checks pass.
	code, out, errOut = cliRun(t, "portable", "doctor", "--host", "openclaw:myns",
		"--compare-with", pkgPath, "--json")
	if code != 0 {
		t.Fatalf("doctor json: code=%d out=%q err=%q", code, out, errOut)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("doctor json parse: %v\n%s", err, out)
	}
	if report["host_kind"] != "openclaw" || report["namespace"] != "myns" || report["all_passed"] != true {
		t.Fatalf("doctor json = %v", report)
	}
	for _, c := range report["checks"].([]any) {
		if c.(map[string]any)["passed"] != true {
			t.Fatalf("doctor check failed: %v", c)
		}
	}
	if report["raw_event_count"] != float64(3) || report["entity_count"] != float64(1) {
		t.Fatalf("doctor counts = %v", report)
	}

	// Text doctor: banner + all-passed marker.
	code, out, errOut = cliRun(t, "portable", "doctor", "--host", "openclaw:myns")
	if code != 0 ||
		!strings.Contains(out, "🔍 doctor report: openclaw:myns\n") ||
		!strings.Contains(out, "  ✓ db_accessible") ||
		!strings.Contains(out, "  ✓ schema_version") ||
		!strings.HasSuffix(out, "✅ all checks passed\n") {
		t.Fatalf("doctor text: code=%d out=%q err=%q", code, out, errOut)
	}

	// A missing store fails db_accessible and exits 1 in both modes.
	code, out, errOut = cliRun(t, "portable", "doctor", "--host", "agent:ghost", "--json")
	if code != 1 {
		t.Fatalf("doctor ghost json: code=%d out=%q err=%q", code, out, errOut)
	}
	var ghost map[string]any
	_ = json.Unmarshal([]byte(out), &ghost)
	if ghost["all_passed"] != false {
		t.Fatalf("ghost report = %v", ghost)
	}
	code, out, errOut = cliRun(t, "portable", "doctor", "--host", "agent:ghost")
	if code != 1 ||
		!strings.Contains(out, "  ✗ db_accessible") ||
		!strings.Contains(errOut, "❌ 1 check(s) failed: db_accessible\n") {
		t.Fatalf("doctor ghost text: code=%d out=%q err=%q", code, out, errOut)
	}

	// Missing required --host.
	code, _, errOut = cliRun(t, "portable", "doctor")
	if code != 2 || !strings.Contains(errOut, "Missing option '--host'.") {
		t.Fatalf("doctor no host: code=%d err=%q", code, errOut)
	}
}
