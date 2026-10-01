package memory

// Config CLI commands, ported from adapters/cli/config.py.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// cliGetMemory builds a Memory instance from resolved configuration.
//
// Resolution order (highest priority first):
//  1. CLI flags (--backend, --db, --dsn)
//  2. Environment variables (handled during option parsing)
//  3. Config file (~/.octop-memory/config.json)
//  4. Defaults
func cliGetMemory(c *cliContext) (*Memory, error) {
	backend := c.optStr("backend")
	namespace := c.optStr("namespace")
	db := c.optStr("db")
	_ = c.optStr("dsn")

	if backend != "sqlite" {
		// The Go port ships the sqlite backend only.
		return nil, cliFailf("unsupported backend: %s", backend)
	}
	return NewMemory(namespace, WithMemoryDBPath(expandHomePath(db)))
}

func cliConfigGroup() *cliGroup {
	return &cliGroup{
		name: "config",
		help: "Manage CLI configuration.",
		commands: []*cliCommand{
			{
				name: "show",
				help: "Display the resolved configuration.",
				run:  cliConfigShow,
			},
			{
				name: "set",
				help: "Set a configuration value.",
				args: []cliArg{{name: "KEY"}, {name: "VALUE"}},
				run:  cliConfigSet,
			},
			{
				name: "path",
				help: "Print the config file path.",
				run:  cliConfigPath,
			},
		},
	}
}

func cliConfigShow(c *cliContext, opts map[string]any, args []string) error {
	outputJSON := c.optBool("output_json")
	configFile := GetCLIConfigPath()
	fileConfig := LoadCLIConfig()

	// Show effective config (with overrides).
	effective := ord(
		cliPair{"backend", c.optStr("backend")},
		cliPair{"namespace", c.optStr("namespace")},
		cliPair{"config_file", configFile},
		cliPair{"file_config", fileConfig},
	)
	backend := c.optStr("backend")
	if backend == "sqlite" {
		effective = append(effective,
			cliPair{"sqlite", ord(cliPair{"db_path", c.optStr("db")})})
	} else if backend == "postgres" {
		effective = append(effective,
			cliPair{"postgres", ord(cliPair{"dsn", c.optStr("dsn")})})
	}

	if outputJSON {
		c.echo(pyJSON(ord(
			cliPair{"status", "ok"},
			cliPair{"data", effective},
		), true, "  "))
		return nil
	}
	c.echo(fmt.Sprintf("Config file: %s", configFile))
	c.echo(fmt.Sprintf("Backend:     %s", backend))
	c.echo(fmt.Sprintf("Namespace:   %s", c.optStr("namespace")))
	if backend == "sqlite" {
		c.echo(fmt.Sprintf("DB path:     %s", c.optStr("db")))
	} else if backend == "postgres" {
		c.echo(fmt.Sprintf("DSN:         %s", c.optStr("dsn")))
	}
	c.echo(fmt.Sprintf("\nFile contents (%s):", configFile))
	if _, err := os.Stat(configFile); err == nil {
		out, err := json.MarshalIndent(fileConfig, "", "  ")
		if err != nil {
			return err
		}
		c.echo(string(out))
	} else {
		c.echo("  (no config file found)")
	}
	return nil
}

func cliConfigSet(c *cliContext, opts map[string]any, args []string) error {
	key, value := args[0], args[1]
	fileConfig := LoadCLIConfig()

	// Handle dotted keys: e.g. 'sqlite.db_path', 'postgres.dsn'.
	section, subkey, dotted := strings.Cut(key, ".")
	if dotted {
		existing, _ := fileConfig[section].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		existing[subkey] = value
		fileConfig[section] = existing
	} else {
		fileConfig[key] = value
	}
	if err := SaveCLIConfig(fileConfig); err != nil {
		return err
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(ord(
			cliPair{"status", "ok"},
			cliPair{"data", ord(cliPair{"key", key}, cliPair{"value", value})},
		), true, ""))
		return nil
	}
	c.echo(fmt.Sprintf("Set %s = %s", key, value))
	return nil
}

func cliConfigPath(c *cliContext, opts map[string]any, args []string) error {
	c.echo(GetCLIConfigPath())
	return nil
}
