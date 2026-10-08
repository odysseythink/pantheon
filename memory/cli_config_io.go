package memory

// Plain config IO, ported from _config_io.py (no CLI framework dependency).
//
// Provides load/save for ~/.octop-memory/config.json. The path override is a
// process-wide mutable holder, mirroring the Python module singleton.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

var (
	cliConfigDir  = filepath.Join(expandHomePath("~"), ".octop-memory")
	cliConfigFile = filepath.Join(cliConfigDir, "config.json")
)

var cliConfigPathMu struct {
	sync.Mutex
	override string
}

// SetCLIConfigPath sets the config file path override (CLI --config flag).
// Pass "" to clear the override.
func SetCLIConfigPath(path string) {
	cliConfigPathMu.Lock()
	defer cliConfigPathMu.Unlock()
	cliConfigPathMu.override = path
}

// GetCLIConfigPath returns the effective config file path.
func GetCLIConfigPath() string {
	cliConfigPathMu.Lock()
	defer cliConfigPathMu.Unlock()
	if cliConfigPathMu.override != "" {
		return cliConfigPathMu.override
	}
	return cliConfigFile
}

var cliConfigDefaults = map[string]any{
	"backend":   "sqlite",
	"namespace": "default",
	"sqlite": map[string]any{
		"db_path": "~/.octop-memory/memory.db",
	},
	"postgres": map[string]any{
		"dsn": "postgresql://localhost/octop_memory",
	},
	"qdrant": map[string]any{
		"url": "http://localhost:6333",
	},
}

// LoadCLIConfig loads the config file, deep-merged over the defaults.
// Corrupt or unreadable files fall back to the defaults (suppressed, as in
// Python).
func LoadCLIConfig() map[string]any {
	config := map[string]any{}
	for k, v := range cliConfigDefaults {
		config[k] = v
	}
	path := GetCLIConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	var userConfig map[string]any
	if err := json.Unmarshal(raw, &userConfig); err != nil {
		return config
	}
	for key, value := range userConfig {
		// Deep merge for backend-specific sections.
		if vm, ok := value.(map[string]any); ok {
			if base, ok := config[key].(map[string]any); ok {
				merged := map[string]any{}
				for bk, bv := range base {
					merged[bk] = bv
				}
				for uk, uv := range vm {
					merged[uk] = uv
				}
				config[key] = merged
				continue
			}
		}
		config[key] = value
	}
	return config
}

// SaveCLIConfig writes the config file with a trailing newline
// (json.dump(indent=2, ensure_ascii=False)).
func SaveCLIConfig(config map[string]any) error {
	path := GetCLIConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
