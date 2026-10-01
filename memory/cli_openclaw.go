package memory

// `octop-memory openclaw` — manage the OpenClaw native plugin slot.
//
// Mirrors src/octop_memory/adapters/cli/openclaw_cmd.py: setup / doctor /
// uninstall / print-config over an openclaw.json.
//
// Divergences from Python, all deliberate:
//   - JSON emitted from map[string]any is key-sorted by encoding/json,
//     while Python dicts keep insertion order (same divergence as
//     cli_migration.go); the payload contents are identical.
//   - doctor's "Python bridge importable" check always passes: the Go port
//     IS the bridge runtime, so the import can never fail while this code
//     is running. FTS5 is probed against the same embedded SQLite driver
//     the storage backend uses.
//   - _resolve_bridge_config falls back to os.Executable() where Python
//     used sys.executable (both are "the interpreter running this CLI").

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	cliOpenclawPluginID  = "octopmemory"
	cliOpenclawDefaultNS = "openclaw__default"
	cliOpenclawNPMGlob   = "npm/projects/*/node_modules/@octop-memory/openclaw"
)

var cliOpenclawProfileChoices = []string{"balanced", "low_latency", "proactive", "privacy", "archive", "eval"}

var cliOpenclawHostFilesAllow = []any{"topics/*.md", "projects/*.md"}

// cliOpenclawHome is DEFAULT_OPENCLAW_HOME (~/.openclaw, expanded lazily).
func cliOpenclawHome() string {
	return expandHomePath("~/.openclaw")
}

func cliOpenclawDefaultConfig() string {
	return filepath.Join(cliOpenclawHome(), "openclaw.json")
}

func cliOpenclawDefaultExtensionsDir() string {
	return filepath.Join(cliOpenclawHome(), "extensions")
}

var cliOpenclawBalancedDefaults = map[string]any{
	"mode":             "self-hosted",
	"profile":          "balanced",
	"host_files_allow": append([]any{}, cliOpenclawHostFilesAllow...),
	"recall": map[string]any{
		"mode":                "tool_hint",
		"default_max_results": 5,
		"default_corpus":      "all",
		"raw_policy":          "fallback",
		"host_files_policy":   "include",
		"citation_policy":     "auto",
		"max_prompt_chars":    1200,
		"layer_order":         []any{"atom", "host_file", "page", "raw"},
	},
	"capture": map[string]any{
		"agent_end_hook":       true,
		"host_files_watcher":   true,
		"min_message_chars":    50,
		"include_roles":        []any{"user", "assistant"},
		"include_tool_calls":   false,
		"include_tool_results": false,
		"skip_memory_echo":     true,
	},
	"privacy": map[string]any{
		"redact_secrets":      true,
		"redact_patterns":     []any{},
		"store_raw_content":   true,
		"store_tool_payloads": false,
	},
	"compaction": map[string]any{
		"enabled":                    true,
		"soft_threshold_tokens":      4000,
		"force_flush_transcript_bytes": 2 * 1024 * 1024,
		"host_file_index_after_flush":  true,
	},
}

var cliOpenclawProfileOverrides = map[string]map[string]any{
	"balanced": {},
	"low_latency": {
		"recall": map[string]any{
			"default_max_results": 3,
			"default_corpus":      "memory",
			"raw_policy":          "never",
			"host_files_policy":   "off",
			"citation_policy":     "off",
			"max_prompt_chars":    400,
			"layer_order":         []any{"atom", "page"},
		},
		"capture": map[string]any{
			"host_files_watcher": false,
			"min_message_chars":  100,
			"include_roles":      []any{"user"},
		},
		"compaction": map[string]any{"enabled": false},
	},
	"proactive": {
		"recall":  map[string]any{"mode": "hybrid", "citation_policy": "always"},
		"capture": map[string]any{"min_message_chars": 30},
	},
	"privacy": {
		"recall": map[string]any{
			"default_corpus":    "memory",
			"raw_policy":        "never",
			"host_files_policy": "off",
			"citation_policy":   "off",
			"layer_order":       []any{"atom", "page"},
		},
		"capture": map[string]any{
			"host_files_watcher": false,
			"min_message_chars":  120,
			"include_roles":      []any{"user"},
		},
		"privacy": map[string]any{"store_raw_content": false, "store_tool_payloads": false},
	},
	"archive": {
		"recall": map[string]any{
			"default_max_results": 10,
			"raw_policy":          "always",
			"citation_policy":     "always",
			"max_prompt_chars":    2000,
			"layer_order":         []any{"atom", "raw", "host_file", "page"},
		},
		"capture": map[string]any{
			"min_message_chars":    0,
			"include_roles":        []any{"user", "assistant", "tool"},
			"include_tool_calls":   true,
			"include_tool_results": true,
		},
		"compaction": map[string]any{"soft_threshold_tokens": 3000},
	},
	"eval": {
		"recall":  map[string]any{"citation_policy": "always", "layer_order": []any{"atom", "page", "raw", "host_file"}},
		"capture": map[string]any{"min_message_chars": 0},
	},
}

func cliOpenclawGroup() *cliGroup {
	return &cliGroup{
		name: "openclaw",
		help: "Configure / inspect the OpenClaw native memory plugin slot.",
		commands: []*cliCommand{
			cliOpenclawSetupCommand(),
			cliOpenclawDoctorCommand(),
			cliOpenclawUninstallCommand(),
			cliOpenclawPrintConfigCommand(),
		},
	}
}

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

func cliOpenclawSetupCommand() *cliCommand {
	return &cliCommand{
		name: "setup",
		help: "Wire plugins.slots.memory: octopmemory into openclaw.json.",
		options: []cliOption{
			{name: "openclaw-config", def: cliOpenclawDefaultConfig(), help: "Path to OpenClaw's openclaw.json config file."},
			{name: "profile", choices: cliOpenclawProfileChoices, def: "balanced", help: "Scenario preset for recall/capture/privacy defaults."},
			{name: "namespace", help: "Memory namespace (default: '" + cliOpenclawDefaultNS + "', always written explicitly so the plugin does not derive its own). Use this to share memory across machines / agents under one identity."},
			{name: "db-path", help: "SQLite path. Defaults to <openclaw-home>/octopmemory/<namespace>/memory.sqlite — under the OpenClaw home because sandboxed deployments (openclaw-security/bwrap) only bind that directory into plugin subprocesses; ~/.octopmemory is invisible there."},
			{name: "workspace", help: "Workspace dir for D51-C host_files watcher. Defaults to ~/.openclaw/workspace or ~/.openclaw/workspace-<OPENCLAW_PROFILE>."},
			{name: "host-files-allow", multiple: true, help: "Additional relative Markdown glob to index under the workspace, e.g. topics/*.md. May be repeated or comma-separated."},
			{name: "no-host-files-watcher", flag: true, help: "Disable host_files watcher even though it is on by default."},
			{name: "bridge-python", help: "Python interpreter the TS plugin should spawn for the bridge subprocess. Default: auto-detected from the environment running this command — the octopmemory-bridge console script next to this interpreter when present (written as bridge.command), else this interpreter itself (bridge.python). Only pass this to point the bridge at a different environment."},
			{name: "recall-mode", choices: []string{"off", "tool_hint", "hybrid"}, def: "tool_hint", help: "Memory prompt mode: off=no memory prompt, tool_hint=tools-only hint, hybrid=hybrid auto-recall mode (currently falls back to explicit tool guidance)."},
			{name: "no-agent-end-hook", flag: true, help: "Disable D52-C agent_end auto-capture (default on)."},
			{name: "dry-run", flag: true, help: "Print the resulting JSON to stdout instead of writing it."},
		},
		run: cliOpenclawSetupRun,
	}
}

// cliOpenclawResolveBridgeConfig mirrors _resolve_bridge_config.
func cliOpenclawResolveBridgeConfig(bridgePython string) map[string]any {
	if bridgePython != "" {
		return map[string]any{"python": bridgePython, "log_level": "info"}
	}
	if exec, err := os.Executable(); err == nil {
		script := filepath.Join(filepath.Dir(exec), "octopmemory-bridge")
		if _, err := os.Stat(script); err == nil {
			return map[string]any{"command": script, "log_level": "info"}
		}
		return map[string]any{"python": exec, "log_level": "info"}
	}
	return map[string]any{"python": "python3", "log_level": "info"}
}

func cliOpenclawLoadOrInit(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		// Python: `if not path.exists(): return {}` — a missing file means
		// an empty config; other read failures are lumped in here too.
		return map[string]any{}, nil
	}
	var loaded map[string]any
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, &cliUsageError{fmt.Sprintf("openclaw config at %s is not valid JSON: %s; refusing to overwrite", path, err)}
	}
	if loaded == nil {
		loaded = map[string]any{}
	}
	return loaded, nil
}

func cliOpenclawDefaultWorkspace() string {
	profile := strings.TrimSpace(os.Getenv("OPENCLAW_PROFILE"))
	if profile != "" && profile != "default" {
		return expandHomePath("~/.openclaw/workspace-" + profile)
	}
	return expandHomePath("~/.openclaw/workspace")
}

// cliOpenclawAbs resolves a workspace path the way
// Path.expanduser().resolve() does for the shapes we accept.
func cliOpenclawAbs(p string) string {
	p = expandHomePath(p)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func cliOpenclawSplitCSV(values []any) []string {
	out := []string{}
	for _, v := range values {
		s, _ := v.(string)
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func cliOpenclawSetupRun(c *cliContext, opts map[string]any, args []string) error {
	openclawConfig := cliOptStr(opts, "openclaw-config")
	if err := cliClickPathFile("--openclaw-config", openclawConfig); err != nil {
		return err
	}
	profile := cliOptStr(opts, "profile")
	namespace := cliOptStr(opts, "namespace")
	dbPath := cliOptStr(opts, "db-path")
	if err := cliClickPathFile("--db-path", dbPath); err != nil {
		return err
	}
	workspace := cliOptStr(opts, "workspace")
	if err := cliClickPathDir("--workspace", workspace); err != nil {
		return err
	}
	noWatcher := cliOptBool(opts, "no-host-files-watcher")
	bridgePython := cliOptStr(opts, "bridge-python")
	recallMode := cliOptStr(opts, "recall-mode")
	noAgentEndHook := cliOptBool(opts, "no-agent-end-hook")
	dryRun := cliOptBool(opts, "dry-run")

	cfg, err := cliOpenclawLoadOrInit(openclawConfig)
	if err != nil {
		return err
	}
	plugins := cliOpenclawMapOrInit(cfg, "plugins")
	slots := cliOpenclawMapOrInit(plugins, "slots")
	entries := cliOpenclawMapOrInit(plugins, "entries")

	slots["memory"] = cliOpenclawPluginID

	pluginCfg := cliOpenclawProfileConfig(profile)
	pluginCfg["namespace"] = namespace
	if namespace == "" {
		pluginCfg["namespace"] = cliOpenclawDefaultNS
	}
	var resolvedDB string
	if dbPath != "" {
		resolvedDB = expandHomePath(dbPath)
	} else {
		resolvedDB = filepath.Join(
			filepath.Dir(openclawConfig), "octopmemory",
			fmt.Sprintf("%v", pluginCfg["namespace"]), "memory.sqlite",
		)
	}
	pluginCfg["db_path"] = resolvedDB
	pluginCfg["bridge"] = cliOpenclawResolveBridgeConfig(bridgePython)
	pluginCfg["recall"].(map[string]any)["mode"] = recallMode
	pluginCfg["capture"].(map[string]any)["agent_end_hook"] = !noAgentEndHook
	patterns := cliOpenclawSplitCSV(opts["host-files-allow"].([]any))
	if len(patterns) == 0 {
		for _, p := range cliOpenclawHostFilesAllow {
			patterns = append(patterns, fmt.Sprintf("%v", p))
		}
	}
	pluginCfg["host_files_allow"] = patterns

	capture := pluginCfg["capture"].(map[string]any)
	if noWatcher {
		capture["host_files_watcher"] = false
		delete(pluginCfg, "host_files_root")
	} else {
		hfw, _ := capture["host_files_watcher"].(bool)
		if workspace != "" || hfw {
			wsRoot := workspace
			if wsRoot == "" {
				wsRoot = cliOpenclawDefaultWorkspace()
			}
			capture["host_files_watcher"] = true
			pluginCfg["host_files_root"] = cliOpenclawAbs(wsRoot)
		} else {
			delete(pluginCfg, "host_files_root")
		}
	}

	entries[cliOpenclawPluginID] = map[string]any{
		"enabled": true,
		"hooks":   map[string]any{"allowConversationAccess": true},
		"config":  pluginCfg,
	}

	payload := pyJSON(cfg, false, "  ")
	if dryRun {
		c.echo(payload)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(openclawConfig), 0o755); err != nil {
		return cliFailf("%s", err)
	}
	// Atomic write so a Ctrl-C in the middle doesn't truncate the user's
	// existing config.
	tmp := openclawConfig + ".tmp"
	if err := os.WriteFile(tmp, []byte(payload+"\n"), 0o644); err != nil {
		return cliFailf("%s", err)
	}
	if err := os.Rename(tmp, openclawConfig); err != nil {
		return cliFailf("%s", err)
	}
	c.echo(fmt.Sprintf("wrote %s", openclawConfig))
	c.echo(fmt.Sprintf("  plugins.slots.memory = %s", cliOpenclawPluginID))
	c.echo(fmt.Sprintf("  plugins.entries.%s.hooks.allowConversationAccess = true", cliOpenclawPluginID))
	c.echo(fmt.Sprintf("  plugins.entries.%s.config.namespace = %v", cliOpenclawPluginID, pluginCfg["namespace"]))
	c.echo(fmt.Sprintf("  plugins.entries.%s.config.db_path = %v", cliOpenclawPluginID, pluginCfg["db_path"]))
	bridgeCfg := pluginCfg["bridge"].(map[string]any)
	bridgeKey := "python"
	if _, has := bridgeCfg["command"]; has {
		bridgeKey = "command"
	}
	c.echo(fmt.Sprintf("  plugins.entries.%s.config.bridge.%s = %v", cliOpenclawPluginID, bridgeKey, bridgeCfg[bridgeKey]))
	if hfw, _ := capture["host_files_watcher"].(bool); hfw {
		c.echo(fmt.Sprintf("  plugins.entries.%s.config.host_files_root = %v", cliOpenclawPluginID, pluginCfg["host_files_root"]))
	}
	c.echo("")
	c.echo("Next steps:")
	c.echo("  1. Install the plugin: openclaw plugins install @octop-memory/openclaw")
	c.echo("     (or from source: cd plugins/openclaw/octopmemory && npm install && npm run build,")
	c.echo(fmt.Sprintf("      then cp -r — not symlink — into %s/%s)", cliOpenclawDefaultExtensionsDir(), cliOpenclawPluginID))
	c.echo("  2. Restart OpenClaw (openclaw gateway restart).")
	c.echo("")
	c.echo(fmt.Sprintf("Verify: octop-memory openclaw doctor --openclaw-config %s", openclawConfig))
	return nil
}

func cliOpenclawMapOrInit(parent map[string]any, key string) map[string]any {
	m, _ := parent[key].(map[string]any)
	if m == nil {
		m = map[string]any{}
		parent[key] = m
	}
	return m
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

func cliOpenclawDoctorCommand() *cliCommand {
	return &cliCommand{
		name: "doctor",
		help: "Sanity-check the octopmemory plugin install.",
		options: []cliOption{
			{name: "openclaw-config", def: cliOpenclawDefaultConfig()},
			{name: "extensions-dir", def: cliOpenclawDefaultExtensionsDir()},
		},
		run: cliOpenclawDoctorRun,
	}
}

func cliOpenclawDoctorRun(c *cliContext, opts map[string]any, args []string) error {
	openclawConfig := cliOptStr(opts, "openclaw-config")
	extensionsDir := cliOptStr(opts, "extensions-dir")
	failed := []string{}

	ok := func(msg string) { c.echo(fmt.Sprintf("  [OK]   %s", msg)) }
	fail := func(msg string) {
		c.echo(fmt.Sprintf("  [FAIL] %s", msg))
		failed = append(failed, msg)
	}
	warn := func(msg string) { c.echo(fmt.Sprintf("  [WARN] %s", msg)) }

	c.echo(fmt.Sprintf("Checking %s ...", openclawConfig))
	var cfg map[string]any
	if _, err := os.Stat(openclawConfig); os.IsNotExist(err) {
		fail(fmt.Sprintf("openclaw config not found: %s", openclawConfig))
	} else {
		ok(fmt.Sprintf("openclaw config exists (%s)", openclawConfig))
		// mirrors: cfg = json.loads(read_text(...)) catching
		// (json.JSONDecodeError, OSError) → fail(...) + cfg = None.
		raw, rerr := os.ReadFile(openclawConfig)
		var parsed map[string]any
		jerr := json.Unmarshal(raw, &parsed)
		if rerr != nil || jerr != nil {
			perr := error(jerr)
			if rerr != nil {
				perr = rerr
			}
			fail(fmt.Sprintf("openclaw config not parseable: %s", perr))
		} else {
			cfg = parsed
		}
		if cfg != nil {
			slot := cliOpenclawGetPath(cfg, "plugins", "slots", "memory")
			if slot == cliOpenclawPluginID {
				ok(fmt.Sprintf("plugins.slots.memory = %s", cliOpenclawPluginID))
			} else if slot == nil {
				fail("plugins.slots.memory is unset (run `octop-memory openclaw setup`)")
			} else {
				warn(fmt.Sprintf("plugins.slots.memory = %s (not %s)", pyReprScalar(slot), pyReprScalar(cliOpenclawPluginID)))
			}
			entryRaw := cliOpenclawGetPath(cfg, "plugins", "entries", cliOpenclawPluginID)
			if entryRaw == nil {
				warn(fmt.Sprintf("plugins.entries.%s missing — slot points at us but no config block", cliOpenclawPluginID))
			} else if entry, isMap := entryRaw.(map[string]any); isMap {
				if enabled, isBool := entry["enabled"].(bool); isBool && !enabled {
					fail(fmt.Sprintf("plugins.entries.%s.enabled = false", cliOpenclawPluginID))
				} else {
					ok(fmt.Sprintf("plugins.entries.%s.enabled = true", cliOpenclawPluginID))
					hooksCfg, isMap := entry["hooks"].(map[string]any)
					if isMap && hooksCfg["allowConversationAccess"] == true {
						ok(fmt.Sprintf("plugins.entries.%s.hooks.allowConversationAccess = true", cliOpenclawPluginID))
					} else {
						fail(fmt.Sprintf(
							"plugins.entries.%s.hooks.allowConversationAccess is not true — "+
								"OpenClaw >=2026.4.29 silently drops agent_end auto-capture without this "+
								"opt-in (re-run `octop-memory openclaw setup`)", cliOpenclawPluginID))
					}
				}
			} else {
				ok(fmt.Sprintf("plugins.entries.%s.enabled = true", cliOpenclawPluginID))
			}
		}
	}

	// The plugin lives either in the manual-install extensions dir or wherever
	// `openclaw plugins install` put it (~/.openclaw/npm/projects/<hash>/...).
	configParent := filepath.Dir(openclawConfig)
	npmCandidates, _ := filepath.Glob(filepath.Join(configParent, filepath.FromSlash(cliOpenclawNPMGlob)))
	sort.Strings(npmCandidates)
	extCandidate := filepath.Join(extensionsDir, cliOpenclawPluginID)
	pluginDir := ""
	for _, cand := range append([]string{extCandidate}, npmCandidates...) {
		if _, err := os.Stat(cand); err == nil {
			pluginDir = cand
			break
		}
	}
	c.echo(fmt.Sprintf("\nChecking plugin directory (%s) ...", extCandidate))
	if pluginDir == "" {
		fail(fmt.Sprintf(
			"plugin directory missing: %s (also checked %s)",
			extCandidate, filepath.Join(configParent, filepath.FromSlash(cliOpenclawNPMGlob))))
	} else {
		symlinked := false
		if fi, err := os.Lstat(pluginDir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			symlinked = true
		}
		if symlinked {
			fail("plugin directory is a symlink — OpenClaw v2026.4.11+ rejects these")
		} else {
			ok(fmt.Sprintf("plugin directory exists (%s)", pluginDir))
			manifestPath := filepath.Join(pluginDir, "openclaw.plugin.json")
			if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
				fail(fmt.Sprintf("manifest missing: %s", manifestPath))
			} else {
				raw, rerr := os.ReadFile(manifestPath)
				var manifest map[string]any
				jerr := json.Unmarshal(raw, &manifest)
				if jerr != nil || rerr != nil {
					perr := jerr
					if rerr != nil {
						perr = rerr
					}
					fail(fmt.Sprintf("manifest not parseable: %s", perr))
				} else if manifest["kind"] == "memory" && manifest["id"] == cliOpenclawPluginID {
					ok(fmt.Sprintf("manifest kind=memory id=%s", cliOpenclawPluginID))
				} else {
					fail(fmt.Sprintf("manifest kind/id mismatch: %s / %s",
						pyReprScalar(manifest["id"]), pyReprScalar(manifest["kind"])))
				}
			}
			indexPath := pluginDir + "/dist/index.js"
			if _, err := os.Stat(indexPath); os.IsNotExist(err) {
				warn(fmt.Sprintf("%s not built — run `npm install && npm run build`", indexPath))
			} else {
				ok("plugin TS shell is built (dist/index.js present)")
			}
		}
	}

	c.echo("\nChecking Python bridge ...")
	// The Go port IS the bridge runtime — its modules are always "importable"
	// while this command runs, unlike the Python CLI where a broken install
	// could fail this check.
	ok(fmt.Sprintf("octop_memory %s importable (bridge protocol %s)", PackageVersion, ProtocolVersion))

	// FTS5 is a compile-time SQLite feature: the whole recall path is
	// FTS5-backed, so a build without it cannot run at all. Probe the same
	// embedded driver the storage backend uses.
	sqliteVer := ""
	var ftsErr error
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		ftsErr = err
	} else {
		defer conn.Close()
		if _, ftsErr = conn.Exec("CREATE VIRTUAL TABLE _probe_fts USING fts5(x)"); ftsErr == nil {
			_ = conn.QueryRow("SELECT sqlite_version()").Scan(&sqliteVer)
		}
	}
	if ftsErr == nil {
		ok(fmt.Sprintf("SQLite %s with FTS5", sqliteVer))
	} else {
		fail(fmt.Sprintf(
			"this build's SQLite %s has no FTS5 module "+
				"→ use a python.org / Homebrew / conda interpreter, or point bridge.command/python "+
				"at a venv whose sqlite3 has FTS5", sqliteVer))
	}

	c.echo("")
	if len(failed) > 0 {
		c.echo(fmt.Sprintf("❌ %d check(s) failed.", len(failed)))
		return errCLIExit1
	}
	c.echo("✅ All required checks passed.")
	return nil
}

// cliOpenclawGetPath walks nested maps like
// cfg.get("a", {}).get("b", {}).get("c"); any missing/non-map level yields nil.
func cliOpenclawGetPath(cfg map[string]any, keys ...string) any {
	var cur any = cfg
	for _, k := range keys {
		m, isMap := cur.(map[string]any)
		if !isMap {
			return nil
		}
		cur = m[k]
	}
	return cur
}

// ---------------------------------------------------------------------------
// uninstall
// ---------------------------------------------------------------------------

func cliOpenclawUninstallCommand() *cliCommand {
	return &cliCommand{
		name: "uninstall",
		help: "Remove octopmemory from plugins.slots.memory.",
		options: []cliOption{
			{name: "openclaw-config", def: cliOpenclawDefaultConfig()},
			{name: "yes", flag: true, help: "Confirm removal (click confirmation_option)."},
		},
		run: cliOpenclawUninstallRun,
	}
}

func cliOpenclawUninstallRun(c *cliContext, opts map[string]any, args []string) error {
	openclawConfig := cliOptStr(opts, "openclaw-config")
	if err := cliClickPathFile("--openclaw-config", openclawConfig); err != nil {
		return err
	}
	if !cliOptBool(opts, "yes") {
		// mirrors click.confirm(prompt, abort=True): prompt on stdout, read
		// one line, Abort ("Aborted!" on stderr, exit 1) unless y/yes.
		fmt.Fprintf(c.stdout, "Remove the %s slot binding? (your stored memory data is NOT deleted.) [y/N]: ", cliOpenclawPluginID)
		answer := strings.ToLower(strings.TrimSpace(cliOpenclawReadLine(c.stdin)))
		if answer != "y" && answer != "yes" {
			c.echoErr("Aborted!")
			return errCLIExit1
		}
	}

	if _, err := os.Stat(openclawConfig); os.IsNotExist(err) {
		c.echo(fmt.Sprintf("nothing to remove: %s does not exist", openclawConfig))
		return nil
	}
	raw, err := os.ReadFile(openclawConfig)
	if err != nil {
		return cliFailf("%s", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cliFailf("%s", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	slots := cliOpenclawGetPath(cfg, "plugins", "slots")
	slotsMap, _ := slots.(map[string]any)
	if slotsMap == nil {
		slotsMap = map[string]any{}
	}
	if mem, _ := slotsMap["memory"].(string); mem == cliOpenclawPluginID {
		delete(slotsMap, "memory")
		out := pyJSON(cfg, false, "  ") + "\n"
		if err := os.WriteFile(openclawConfig, []byte(out), 0o644); err != nil {
			return cliFailf("%s", err)
		}
		c.echo(fmt.Sprintf("removed plugins.slots.memory (%s) from %s", cliOpenclawPluginID, openclawConfig))
	} else {
		cur, has := slotsMap["memory"]
		if !has {
			cur = nil
		}
		c.echo(fmt.Sprintf("no-op: plugins.slots.memory = %s, not %s",
			pyReprScalar(cur), pyReprScalar(cliOpenclawPluginID)))
	}
	return nil
}

func cliOpenclawReadLine(r interface{ Read([]byte) (int, error) }) string {
	buf := make([]byte, 0, 64)
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n == 0 || err != nil {
			break
		}
		if one[0] == '\n' {
			break
		}
		buf = append(buf, one[0])
	}
	return string(buf)
}

// ---------------------------------------------------------------------------
// print-config
// ---------------------------------------------------------------------------

func cliOpenclawPrintConfigCommand() *cliCommand {
	return &cliCommand{
		name: "print-config",
		help: "Print a sample plugins.entries.octopmemory.config block.",
		options: []cliOption{
			{name: "profile", choices: cliOpenclawProfileChoices, def: "balanced"},
			{name: "namespace", def: cliOpenclawDefaultNS},
		},
		run: cliOpenclawPrintConfigRun,
	}
}

func cliOpenclawPrintConfigRun(c *cliContext, opts map[string]any, args []string) error {
	profile := cliOptStr(opts, "profile")
	namespace := cliOptStr(opts, "namespace")
	pluginCfg := cliOpenclawProfileConfig(profile)
	pluginCfg["namespace"] = namespace
	pluginCfg["bridge"] = map[string]any{"python": "python3", "log_level": "info", "spawn_timeout_ms": 5000}
	if hfw, _ := pluginCfg["capture"].(map[string]any)["host_files_watcher"].(bool); hfw {
		pluginCfg["host_files_root"] = cliOpenclawAbs(cliOpenclawDefaultWorkspace())
	}
	sample := map[string]any{
		"plugins": map[string]any{
			"slots": map[string]any{"memory": cliOpenclawPluginID},
			"entries": map[string]any{
				cliOpenclawPluginID: map[string]any{
					"enabled": true,
					"config":  pluginCfg,
				},
			},
		},
	}
	c.echo(pyJSON(sample, false, "  "))
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func cliOpenclawProfileConfig(profile string) map[string]any {
	cfg := cliOpenclawDeepMerge(cliOpenclawBalancedDefaults, cliOpenclawProfileOverrides[profile])
	cfg["profile"] = profile
	return cfg
}

func cliOpenclawDeepMerge(base, override map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = cliOpenclawCloneJSON(v)
	}
	for k, v := range override {
		vo, vIsMap := v.(map[string]any)
		bo, bIsMap := out[k].(map[string]any)
		if vIsMap && bIsMap {
			out[k] = cliOpenclawDeepMerge(bo, vo)
		} else {
			out[k] = cliOpenclawCloneJSON(v)
		}
	}
	return out
}

func cliOpenclawCloneJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}
