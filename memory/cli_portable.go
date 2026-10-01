package memory

// `octop-memory portable` — cross-host memory migration service CLI.
//
// Mirrors src/octop_memory/adapters/cli/portable_cmd.py: list-sources /
// pack / adopt / doctor over the portable migration core in
// migration_portable.go.

import (
	"context"
	"fmt"
)

func cliPortableGroup() *cliGroup {
	return &cliGroup{
		name: "portable",
		help: "Cross-host memory migration service: pack, adopt, doctor.",
		commands: []*cliCommand{
			cliPortableListSourcesCommand(),
			cliPortablePackCommand(),
			cliPortableAdoptCommand(),
			cliPortableDoctorCommand(),
		},
	}
}

// ---------------------------------------------------------------------------
// list-sources
// ---------------------------------------------------------------------------

func cliPortableListSourcesCommand() *cliCommand {
	return &cliCommand{
		name: "list-sources",
		help: "List every migratable memory store on this machine.",
		options: []cliOption{
			{name: "json", flag: true, help: "Output in JSON format."},
		},
		run: cliPortableListSourcesRun,
	}
}

func cliPortableListSourcesRun(c *cliContext, opts map[string]any, args []string) error {
	outputJSON := cliOptBool(opts, "json")
	sources, err := ListSources(context.Background(), nil)
	if err != nil {
		return cliFailf("%s", err)
	}

	if outputJSON {
		// Python: json.dumps([s.to_dict() for s in sources], indent=2).
		payload := make([]any, 0, len(sources))
		for _, src := range sources {
			payload = append(payload, cliPortableSourceDict(src))
		}
		c.echo(pyJSON(payload, false, "  "))
		return nil
	}

	if len(sources) == 0 {
		c.echo("No migratable memory stores were found.")
		return nil
	}

	c.echo(fmt.Sprintf("Found %d memory store(s):\n", len(sources)))
	for i, src := range sources {
		name := src.AgentName
		if name == "" {
			name = src.Namespace
		}
		c.echo(fmt.Sprintf("  [%d] %s:%s", i+1, src.HostKind, name))
		c.echo(fmt.Sprintf("      namespace:   %s", src.Namespace))
		c.echo(fmt.Sprintf("      db_path:     %s", src.DBPath))
		c.echo(fmt.Sprintf(
			"      counts:      raw_events=%d  atoms=%d  entities=%d  journal=%d",
			src.RawEventCount, src.AtomCount, src.EntityCount, src.JournalCount))
		c.echo(fmt.Sprintf("      schema_ver:  %d", src.SchemaVersion))
		c.echo("")
	}
	return nil
}

// ---------------------------------------------------------------------------
// pack
// ---------------------------------------------------------------------------

func cliPortablePackCommand() *cliCommand {
	return &cliCommand{
		name: "pack",
		help: "Pack the specified agent's memory into a .hmpkg file.",
		options: []cliOption{
			{name: "from", required: true, help: "Source memory store, format: host:name (e.g. agent:my-agent)."},
			{name: "out", help: "Output file path (default: ~/.octop-memory/portable/<ns>-<ts>.hmpkg)."},
		},
		run: cliPortablePackRun,
	}
}

func cliPortablePackRun(c *cliContext, opts map[string]any, args []string) error {
	source := cliOptStr(opts, "from")
	outPath := cliOptStr(opts, "out")

	progress := func(done, total int, phase string) {
		switch phase {
		case "exporting":
			c.echoErr("  Exporting memory...")
		case "packing":
			c.echoErr("  Packing...")
		}
	}

	summary, err := PackPortable(context.Background(), source, outPath, progress)
	if err != nil {
		// Python catches ValueError → "Error: {exc}" on stderr + exit 1.
		return cliFailf("%s", err)
	}

	sizeKB := float64(summary.FileSizeBytes) / 1024
	c.echo(fmt.Sprintf(
		"packed %d rows from %s -> %s (%.1f KB)",
		summary.TotalRows, summary.SourceNamespace, summary.OutPath, sizeKB))
	return nil
}

// ---------------------------------------------------------------------------
// adopt
// ---------------------------------------------------------------------------

func cliPortableAdoptCommand() *cliCommand {
	return &cliCommand{
		name: "adopt",
		help: "Import a .hmpkg file into the target host.",
		args:  []cliArg{{name: "PKG_FILE"}},
		options: []cliOption{
			{name: "as", required: true, help: "Target host, format: host or host:namespace (e.g. openclaw or openclaw:myns). For openclaw without an explicit namespace, the namespace configured in ~/.openclaw/openclaw.json (the one the plugin actually reads) is used when present."},
			{name: "on-conflict", choices: []string{"skip", "replace", "raise"}, def: "skip", help: "Conflict strategy (default: skip)."},
			{name: "host-rewrite", choices: []string{"keep", "target"}, def: "keep", help: "Host-field rewrite strategy (default: keep, preserves the original host)."},
			{name: "dry-run", flag: true, help: "Pre-check only, without actually writing to the target db."},
		},
		run: cliPortableAdoptRun,
	}
}

func cliPortableAdoptRun(c *cliContext, opts map[string]any, args []string) error {
	pkgFile := args[0]
	if err := cliClickPathExists("PKG_FILE", pkgFile); err != nil {
		return err
	}
	targetHost := cliOptStr(opts, "as")
	onConflict := cliOptStr(opts, "on-conflict")
	hostRewrite := cliOptStr(opts, "host-rewrite")
	dryRun := cliOptBool(opts, "dry-run")

	progress := func(done, total int, phase string) {
		if phase == "importing" {
			c.echoErr("  Importing memory...")
		}
	}

	summary, err := AdoptPortable(context.Background(), pkgFile, targetHost, AdoptOptions{
		OnConflict:  OnConflict(onConflict),
		HostRewrite: hostRewrite,
		DryRun:      dryRun,
		Progress:    progress,
	})
	if err != nil {
		return cliFailf("%s", err)
	}

	if summary.AlreadyAdopted {
		c.echo(fmt.Sprintf("already adopted at %s, skipping.", cliPyText(summary.AlreadyAdoptedAt)))
		return nil
	}

	if dryRun {
		c.echo(fmt.Sprintf(
			"[dry-run] would write approximately %d record(s) -> %s (%s)",
			summary.Applied, summary.TargetNamespace, summary.TargetDBPath))
		return nil
	}

	errPart := ""
	if len(summary.Errors) > 0 {
		errPart = fmt.Sprintf(", %d errors", len(summary.Errors))
	}
	c.echo(fmt.Sprintf(
		"adopted: %d inserted, %d skipped%s -> %s (%s)",
		summary.Applied, summary.Skipped, errPart, summary.TargetNamespace, summary.TargetDBPath))
	if len(summary.Errors) > 0 {
		for i, e := range summary.Errors {
			if i >= 5 {
				break
			}
			c.echoErr(fmt.Sprintf("  error: %s", e))
		}
		if len(summary.Errors) > 5 {
			c.echoErr(fmt.Sprintf("  ... %d errors in total", len(summary.Errors)))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

// cliPortableSourceDict mirrors SourceInfo.to_dict() key order.
func cliPortableSourceDict(src *SourceInfo) []cliPair {
	return ord(
		cliPair{"host_kind", src.HostKind},
		cliPair{"db_path", src.DBPath},
		cliPair{"namespace", src.Namespace},
		cliPair{"raw_event_count", src.RawEventCount},
		cliPair{"atom_count", src.AtomCount},
		cliPair{"entity_count", src.EntityCount},
		cliPair{"journal_count", src.JournalCount},
		cliPair{"schema_version", src.SchemaVersion},
		cliPair{"agent_name", src.AgentName},
	)
}

// cliPortableCheckDict mirrors DoctorCheckResult.to_dict().
func cliPortableCheckDict(c DoctorCheckResult) []cliPair {
	return ord(
		cliPair{"name", c.Name},
		cliPair{"passed", c.Passed},
		cliPair{"hint", c.Hint},
		cliPair{"detail", c.Detail},
	)
}

// cliPortableDoctorDict mirrors DoctorReport.to_dict() (all_passed included
// in the Python position, after checks).
func cliPortableDoctorDict(r *DoctorReport) []cliPair {
	checks := make([]any, 0, len(r.Checks))
	for _, c := range r.Checks {
		checks = append(checks, cliPortableCheckDict(c))
	}
	return ord(
		cliPair{"host_kind", r.HostKind},
		cliPair{"namespace", r.Namespace},
		cliPair{"db_path", r.DBPath},
		cliPair{"checks", checks},
		cliPair{"all_passed", r.AllPassed()},
		cliPair{"raw_event_count", r.RawEventCount},
		cliPair{"atom_count", r.AtomCount},
		cliPair{"entity_count", r.EntityCount},
		cliPair{"journal_count", r.JournalCount},
	)
}

func cliPortableDoctorCommand() *cliCommand {
	return &cliCommand{
		name: "doctor",
		help: "Check the health status of the target db.",
		options: []cliOption{
			{name: "host", required: true, help: "Target host, format: host or host:namespace (e.g. openclaw:myns). For openclaw without an explicit namespace, the plugin's configured namespace from ~/.openclaw/openclaw.json is used when present."},
			{name: "compare-with", help: "Compare row counts against the manifest of the given .hmpkg file."},
			{name: "db", help: "Directly specify the db path (overrides auto-resolution)."},
			{name: "json", flag: true, help: "Output in JSON format."},
		},
		run: cliPortableDoctorRun,
	}
}

func cliPortableDoctorRun(c *cliContext, opts map[string]any, args []string) error {
	hostSpec := cliOptStr(opts, "host")
	compareWith := cliOptStr(opts, "compare-with")
	if compareWith != "" {
		if err := cliClickPathExists("--compare-with", compareWith); err != nil {
			return err
		}
	}
	dbPath := cliOptStr(opts, "db")
	outputJSON := cliOptBool(opts, "json")

	report := DoctorPortable(context.Background(), hostSpec, "", DoctorOptions{
		CompareWith: compareWith,
		DBPath:      dbPath,
	})

	if outputJSON {
		c.echo(pyJSON(cliPortableDoctorDict(report), false, "  "))
		if !report.AllPassed() {
			return errCLIExit1
		}
		return nil
	}

	// Human-readable output
	c.echo(fmt.Sprintf("\n🔍 doctor report: %s:%s", report.HostKind, report.Namespace))
	c.echo(fmt.Sprintf("   db: %s", report.DBPath))
	c.echo(fmt.Sprintf(
		"   counts: raw_events=%d  atoms=%d  entities=%d  journal=%d",
		report.RawEventCount, report.AtomCount, report.EntityCount, report.JournalCount))
	c.echo("")

	for _, check := range report.Checks {
		icon := "✓"
		if !check.Passed {
			icon = "✗"
		}
		line := fmt.Sprintf("  %s %s", icon, check.Name)
		if check.Detail != "" {
			line += fmt.Sprintf("  (%s)", check.Detail)
		}
		c.echo(line)
		if !check.Passed && check.Hint != "" {
			c.echoErr(fmt.Sprintf("    -> %s", check.Hint))
		}
	}

	c.echo("")
	if report.AllPassed() {
		c.echo("✅ all checks passed")
		return nil
	}
	failed := []string{}
	for _, check := range report.Checks {
		if !check.Passed {
			failed = append(failed, check.Name)
		}
	}
	c.echoErr(fmt.Sprintf("❌ %d check(s) failed: %s", len(failed), cliJoinComma(failed)))
	return errCLIExit1
}

// cliJoinComma renders ", ".join(items).
func cliJoinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
