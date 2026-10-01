package memory

// CLI framework, ported from adapters/cli/ (built on click in Python).
//
// This is a hand-rolled click subset covering exactly what the octop-memory
// CLI uses: command groups, options (--name value / --name=value / -b value),
// boolean flags, envvar fallbacks (CLI flag > env var > default), required
// options, case-insensitive choices (normalized to the canonical choice),
// positional arguments, --help on every level, and --version on the root.
//
// Output fidelity: Python emits json.dumps(...) defaults (", "/": "
// separators, ensure_ascii=True) and insertion-ordered dicts, so rendering
// goes through pyJSON over ordered cliPair objects rather than encoding/json
// (which sorts map keys and compacts separators).

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ---------------------------------------------------------------------------
// Python-style JSON rendering (json.dumps subset)
// ---------------------------------------------------------------------------

// cliPair is one key/value entry of an ordered JSON object.
type cliPair struct {
	K string
	V any
}

// ord builds an ordered object for pyJSON.
func ord(pairs ...cliPair) []cliPair { return pairs }

func pyJSON(v any, ensureASCII bool, indent string) string {
	var b strings.Builder
	cliWriteJSON(&b, v, ensureASCII, indent, 0)
	return b.String()
}

func cliWriteIndent(b *strings.Builder, indent string, depth int) {
	if indent == "" {
		return
	}
	b.WriteString(strings.Repeat(indent, depth))
}

func cliWriteNewline(b *strings.Builder, indent string) {
	if indent != "" {
		b.WriteString("\n")
	}
}

func cliWriteJSON(b *strings.Builder, v any, ensureASCII bool, indent string, depth int) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteString(pyJSONString(t, ensureASCII))
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		b.WriteString(pyFloatRepr(t))
	case []cliPair:
		if len(t) == 0 {
			b.WriteString("{}")
			return
		}
		sep := ", "
		if indent != "" {
			sep = ","
		}
		b.WriteString("{")
		for i, p := range t {
			if i > 0 {
				b.WriteString(sep)
			}
			cliWriteNewline(b, indent)
			cliWriteIndent(b, indent, depth+1)
			b.WriteString(pyJSONString(p.K, ensureASCII))
			b.WriteString(": ")
			cliWriteJSON(b, p.V, ensureASCII, indent, depth+1)
		}
		cliWriteNewline(b, indent)
		cliWriteIndent(b, indent, depth)
		b.WriteString("}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		sep := ", "
		if indent != "" {
			sep = ","
		}
		b.WriteString("[")
		for i, e := range t {
			if i > 0 {
				b.WriteString(sep)
			}
			cliWriteNewline(b, indent)
			cliWriteIndent(b, indent, depth+1)
			cliWriteJSON(b, e, ensureASCII, indent, depth+1)
		}
		cliWriteNewline(b, indent)
		cliWriteIndent(b, indent, depth)
		b.WriteString("]")
	case []string:
		anySlice := make([]any, len(t))
		for i, e := range t {
			anySlice[i] = e
		}
		cliWriteJSON(b, anySlice, ensureASCII, indent, depth)
	case map[string]any:
		// Go maps are unordered; render with sorted keys for determinism.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]cliPair, 0, len(t))
		for _, k := range keys {
			pairs = append(pairs, cliPair{K: k, V: t[k]})
		}
		cliWriteJSON(b, pairs, ensureASCII, indent, depth)
	default:
		fmt.Fprintf(b, "%v", t)
	}
}

// pyJSONString escapes like json.dumps: control chars \uXXXX (lowercase),
// and with ensure_ascii=True all non-ASCII becomes \uXXXX (surrogate pairs).
func pyJSONString(s string, ensureASCII bool) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			if !ensureASCII || r < 0x80 {
				b.WriteRune(r)
				continue
			}
			if r > 0xFFFF {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pyFloatRepr mirrors Python repr(float): shortest round-trip form, with
// ".0" appended to integral values ("1" → "1.0").
func pyFloatRepr(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// ---------------------------------------------------------------------------
// Framework types
// ---------------------------------------------------------------------------

type cliContext struct {
	obj    map[string]any
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c *cliContext) echo(s string)      { fmt.Fprintln(c.stdout, s) }
func (c *cliContext) echoErr(s string)   { fmt.Fprintln(c.stderr, s) }
func (c *cliContext) readAll() string    { raw, _ := io.ReadAll(c.stdin); return string(raw) }
func (c *cliContext) optBool(k string) bool { v, _ := c.obj[k].(bool); return v }
func (c *cliContext) optStr(k string) string { v, _ := c.obj[k].(string); return v }

// cliOptStr reads a parsed option value as a string; nil/absent → "".
func cliOptStr(opts map[string]any, key string) string {
	v, _ := opts[key].(string)
	return v
}

// cliOptBool reads a parsed command-level flag; nil/absent → false.
// (c.optBool only sees the ROOT global options in ctx.obj.)
func cliOptBool(opts map[string]any, key string) bool {
	v, _ := opts[key].(bool)
	return v
}

// cliUsageError aborts with exit code 2 (click.UsageError).
type cliUsageError struct{ msg string }

func (e *cliUsageError) Error() string { return e.msg }

// cliClickException aborts with exit code 1 and "Error: ..." on stderr
// (click.ClickException).
type cliClickException struct{ msg string }

func (e *cliClickException) Error() string { return e.msg }

func cliFailf(format string, args ...any) error {
	return &cliClickException{msg: fmt.Sprintf(format, args...)}
}

type cliOption struct {
	name     string // long name without "--"
	offName  string // for --use-llm/--no-llm pairs: the "--no-…" name without "--"
	short    string // single letter without "-" ("" if none)
	envvar   string
	flag     bool
	multiple bool // repeatable option; value accumulates into []any
	hidden   bool // excluded from help output (click hidden=True)
	required bool
	def      any
	choices  []string
	isInt    bool
	isFloat  bool
	help     string
}

type cliArg struct {
	name     string // uppercase metavar, e.g. "NODE_ID"
	optional bool   // click required=False argument
}

type cliCommand struct {
	name    string
	help    string
	options []cliOption
	args    []cliArg
	run     func(c *cliContext, opts map[string]any, args []string) error
}

type cliGroup struct {
	name     string
	help     string
	commands []*cliCommand
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

func cliFindOption(cmd *cliCommand, long string) *cliOption {
	for i := range cmd.options {
		if cmd.options[i].name == long {
			return &cmd.options[i]
		}
	}
	return nil
}

func cliFindShort(cmd *cliCommand, short string) *cliOption {
	for i := range cmd.options {
		if cmd.options[i].short != "" && cmd.options[i].short == short {
			return &cmd.options[i]
		}
	}
	return nil
}

func cliCoerceOption(o *cliOption, key, val string) (any, error) {
	if len(o.choices) > 0 {
		for _, ch := range o.choices {
			if strings.EqualFold(val, ch) {
				return ch, nil // case-insensitive: normalize to the choice
			}
		}
		return nil, &cliUsageError{fmt.Sprintf(
			"Invalid value for '%s': '%s' is not one of %s.", key, val, cliChoiceRepr(o.choices))}
	}
	if o.isInt {
		n, err := strconv.Atoi(val)
		if err != nil {
			return nil, &cliUsageError{fmt.Sprintf(
				"Invalid value for '%s': '%s' is not a valid integer.", key, val)}
		}
		return n, nil
	}
	if o.isFloat {
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil, &cliUsageError{fmt.Sprintf(
				"Invalid value for '%s': '%s' is not a valid floating point value.", key, val)}
		}
		return f, nil
	}
	return val, nil
}

// cliChoiceRepr renders choices like click's message: (leaf, branch, root).
func cliChoiceRepr(choices []string) string {
	quoted := make([]string, len(choices))
	for i, c := range choices {
		quoted[i] = "'" + c + "'"
	}
	return strings.Join(quoted, ", ")
}

// cliParseOptions parses option tokens; returns the option map and the
// positional arguments. Pre-fills defaults and envvar fallbacks so callers
// observe the CLI flag > env var > default precedence.
func cliParseOptions(cmd *cliCommand, tokens []string) (map[string]any, []string, error) {
	opts := map[string]any{}
	provided := map[string]bool{}
	for i := range cmd.options {
		o := &cmd.options[i]
		v := o.def
		if o.multiple {
			// click multiple=True defaults to an empty tuple.
			v = []any{}
		}
		if !o.flag && o.envvar != "" {
			if ev, ok := os.LookupEnv(o.envvar); ok && ev != "" {
				v = ev
				provided[o.name] = true
			}
		}
		opts[o.name] = v
	}

	var positionals []string
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case strings.HasPrefix(tok, "--"):
			name, inline, hasInline := strings.Cut(tok[2:], "=")
			o := cliFindOption(cmd, name)
			if o == nil && strings.HasPrefix(name, "no-") {
				// --use-llm/--no-llm style boolean pair.
				if base := cliFindOption(cmd, name[3:]); base != nil && base.offName == name {
					opts[base.name] = false
					provided[base.name] = true
					continue
				}
			}
			if o == nil {
				return nil, nil, &cliUsageError{"No such option: --" + name}
			}
			key := "--" + name
			if o.flag {
				opts[o.name] = true
				provided[o.name] = true
				continue
			}
			val := inline
			if !hasInline {
				i++
				if i >= len(tokens) {
					return nil, nil, &cliUsageError{fmt.Sprintf("Option '%s' requires an argument.", key)}
				}
				val = tokens[i]
			}
			cv, err := cliCoerceOption(o, key, val)
			if err != nil {
				return nil, nil, err
			}
			if o.multiple {
				acc, _ := opts[o.name].([]any)
				opts[o.name] = append(acc, cv)
			} else {
				opts[o.name] = cv
			}
			provided[o.name] = true
		case len(tok) > 1 && tok[0] == '-' && !strings.HasPrefix(tok, "--"):
			o := cliFindShort(cmd, tok[1:])
			if o == nil {
				return nil, nil, &cliUsageError{"No such option: -" + tok[1:]}
			}
			if o.flag {
				opts[o.name] = true
				provided[o.name] = true
				continue
			}
			i++
			if i >= len(tokens) {
				return nil, nil, &cliUsageError{fmt.Sprintf("Option '-%s' requires an argument.", o.short)}
			}
			cv, err := cliCoerceOption(o, "-"+o.short, tokens[i])
			if err != nil {
				return nil, nil, err
			}
			if o.multiple {
				acc, _ := opts[o.name].([]any)
				opts[o.name] = append(acc, cv)
			} else {
				opts[o.name] = cv
			}
			provided[o.name] = true
		default:
			positionals = append(positionals, tok)
		}
	}

	for i := range cmd.options {
		o := &cmd.options[i]
		if o.required && !provided[o.name] {
			return nil, nil, &cliUsageError{fmt.Sprintf("Missing option '--%s'.", o.name)}
		}
	}

	for idx, a := range cmd.args {
		if a.optional {
			continue
		}
		if idx >= len(positionals) {
			return nil, nil, &cliUsageError{fmt.Sprintf("Missing argument '%s'.", a.name)}
		}
	}
	if len(positionals) > len(cmd.args) {
		extra := positionals[len(cmd.args):]
		return nil, nil, &cliUsageError{fmt.Sprintf(
			"Got unexpected extra argument (%s).", strings.Join(extra, ", "))}
	}
	return opts, positionals, nil
}

func cliHasHelpToken(tokens []string) bool {
	for _, t := range tokens {
		if t == "--help" || t == "-h" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Help rendering (click-flavored, abbreviated)
// ---------------------------------------------------------------------------

func cliOptionUsage(o *cliOption) string {
	if o.short != "" {
		return fmt.Sprintf("  -%s, --%s", o.short, o.name)
	}
	return fmt.Sprintf("  --%s", o.name)
}

func cliGroupHelp(g *cliGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: octop-memory %s [OPTIONS] COMMAND [ARGS]...\n\n  %s\n\nCommands:\n", g.name, g.help)
	for _, cmd := range g.commands {
		fmt.Fprintf(&b, "  %s  %s\n", cmd.name, cmd.help)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// cliMainSpec mirrors the @click.group() root: global options shared by every
// subcommand through ctx.obj.
func cliMainSpec() *cliCommand {
	return &cliCommand{
		name: "octop-memory",
		help: "octop-memory: CLI for the pluggable memory system.",
		options: []cliOption{
			{name: "config", envvar: "OCTOP_MEMORY_CONFIG", help: "Path to config file (default: ~/.octop-memory/config.json)."},
			{name: "backend", short: "b", envvar: "OCTOP_MEMORY_BACKEND", choices: []string{"sqlite", "postgres"}, help: "Storage backend type."},
			{name: "db", envvar: "OCTOP_MEMORY_DB", help: "SQLite database path."},
			{name: "dsn", envvar: "OCTOP_MEMORY_DSN", help: "PostgreSQL connection string."},
			{name: "namespace", short: "n", envvar: "OCTOP_MEMORY_NAMESPACE", help: "Memory namespace."},
			{name: "json", flag: true, def: false, help: "Output as JSON."},
		},
	}
}

// cliEntry is one root-level registration: either a command group or a
// bare top-level command (click's add_command accepts both).
type cliEntry struct {
	group *cliGroup
	cmd   *cliCommand
}

// cliRegistry mirrors adapters/cli/__init__.py's add_command order.
// dashboard registers unconditionally (Python gates it on fastapi being
// importable; the Go dashboard server has no optional dependency).
func cliRegistry() []cliEntry {
	return []cliEntry{
		{group: cliConfigGroup()},
		{group: cliMemoryGroup()},
		{group: cliRawGroup()},
		{group: cliCandidateGroup()},
		{group: cliAtomGroup()},
		{group: cliEntityGroup()},
		{group: cliJournalGroup()},
		{group: cliPageGroup()},
		{cmd: cliRecallCommand()},
		{group: cliThreadGroup()},
		{cmd: cliExportCommand()},
		{cmd: cliImportCommand()},
		{cmd: cliMigrateCommand()},
		{cmd: cliBackfillCommand()},
		{group: cliGcGroup()},
		{group: cliDbGroup()},
		{group: cliConsolidateGroup()},
		{group: cliOpenclawGroup()},
		{group: cliEpisodeGroup()},
		{group: cliDigestGroup()},
		{group: cliPortableGroup()},
		{cmd: cliDashboardCommand()},
	}
}

// cliGroups returns the group entries (kept for direct dispatch helpers).
func cliGroups() []*cliGroup {
	var out []*cliGroup
	for _, e := range cliRegistry() {
		if e.group != nil {
			out = append(out, e.group)
		}
	}
	return out
}

// cliCommandHelpNamed renders a command help with an arbitrary invocation
// path (group.sub or bare top-level command).
func cliCommandHelpNamed(name string, cmd *cliCommand) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: octop-memory %s [OPTIONS]", name)
	for _, a := range cmd.args {
		fmt.Fprintf(&b, " %s", a.name)
	}
	fmt.Fprintf(&b, "\n\n  %s\n", cmd.help)
	if len(cmd.options) > 0 {
		b.WriteString("\nOptions:\n")
		for i := range cmd.options {
			o := &cmd.options[i]
			if o.hidden {
				continue
			}
			fmt.Fprintf(&b, "%s  %s\n", cliOptionUsage(o), o.help)
		}
		b.WriteString("  --help, -h  Show this message and exit.\n")
	}
	return b.String()
}

func cliCommandHelp(g *cliGroup, cmd *cliCommand) string {
	return cliCommandHelpNamed(g.name+" "+cmd.name, cmd)
}

// cliRunCommand parses options for cmd, runs it, and maps errors to exit
// codes (shared by group dispatch and top-level command dispatch).
func cliRunCommand(c *cliContext, cmd *cliCommand, cmdTokens []string) int {
	opts, cmdArgs, err := cliParseOptions(cmd, cmdTokens)
	if err != nil {
		c.echoErr("Error: " + err.Error())
		return 2
	}
	if err := cmd.run(c, opts, cmdArgs); err != nil {
		var ue *cliUsageError
		var ce *cliClickException
		var ex *cliExitCodeError
		switch {
		case err == errCLIExit1:
			return 1
		case err == errCLIExit2:
			return 2
		case asCLIExitCodeError(err, &ex):
			return ex.code
		case asCLIUsageError(err, &ue):
			c.echoErr("Error: " + ue.msg)
			return 2
		case asCLIClickException(err, &ce):
			c.echoErr("Error: " + ce.msg)
			return 1
		default:
			c.echoErr("Error: " + err.Error())
			return 1
		}
	}
	return 0
}

func asCLIExitCodeError(err error, target **cliExitCodeError) bool {
	if ex, ok := err.(*cliExitCodeError); ok {
		*target = ex
		return true
	}
	return false
}

// MainCLI runs the CLI and returns the process exit code.
func MainCLI(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cliContext{obj: map[string]any{}, stdin: stdin, stdout: stdout, stderr: stderr}
	main := cliMainSpec()

	// @click.version_option
	for _, tok := range argv {
		if tok == "--version" {
			c.echo("octop-memory, version " + PackageVersion)
			return 0
		}
	}

	// Split argv into leading global options and the group path remainder.
	// The scan is arity-aware so option VALUES are never mistaken for the
	// group name ("--db X memory store" → global ["--db","X"], rest ["memory",...]).
	globalTokens := argv
	rest := []string{}
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if tok == "--" {
			globalTokens = argv[:i]
			rest = argv[i+1:]
			break
		}
		if strings.HasPrefix(tok, "--") {
			name, _, hasInline := strings.Cut(tok[2:], "=")
			o := cliFindOption(main, name)
			if o == nil {
				c.echoErr("Error: No such option: --" + name)
				return 2
			}
			if !o.flag && !hasInline {
				i++ // consume the value token
			}
			continue
		}
		if len(tok) > 1 && tok[0] == '-' {
			o := cliFindShort(main, tok[1:])
			if o == nil {
				c.echoErr("Error: No such option: -" + tok[1:])
				return 2
			}
			if !o.flag {
				i++
			}
			continue
		}
		globalTokens = argv[:i]
		rest = argv[i:]
		break
	}

	globalOpts, _, err := cliParseOptions(main, globalTokens)
	if err != nil {
		c.echoErr("Error: " + err.Error())
		return 2
	}

	// Set config file path override if provided (--config / env var).
	if configPath, _ := globalOpts["config"].(string); configPath != "" {
		SetCLIConfigPath(configPath)
	}
	fileConfig := LoadCLIConfig()

	fileStr := func(key, def string) string {
		if v, ok := fileConfig[key].(string); ok && v != "" {
			return v
		}
		return def
	}
	fileSection := func(key string) map[string]any {
		m, _ := fileConfig[key].(map[string]any)
		return m
	}
	sectionStr := func(key, sub, def string) string {
		if v, ok := fileSection(key)[sub].(string); ok && v != "" {
			return v
		}
		return def
	}

	// Resolve with priority: CLI flag > env var (parsed above) > config file
	// > defaults. Empty strings count as unset, matching Python `or`.
	backend, _ := globalOpts["backend"].(string)
	if backend == "" {
		backend = fileStr("backend", "sqlite")
	}
	namespace, _ := globalOpts["namespace"].(string)
	if namespace == "" {
		namespace = fileStr("namespace", "default")
	}
	db, _ := globalOpts["db"].(string)
	if backend == "sqlite" {
		if db == "" {
			db = sectionStr("sqlite", "db_path", "~/.octop-memory/memory.db")
		}
	} else if db == "" {
		db = "~/.octop-memory/memory.db"
	}
	dsn, _ := globalOpts["dsn"].(string)
	if backend == "postgres" {
		if dsn == "" {
			dsn = sectionStr("postgres", "dsn", "postgresql://localhost/octop_memory")
		}
	}

	c.obj["backend"] = backend
	c.obj["namespace"] = namespace
	c.obj["db"] = db
	c.obj["dsn"] = dsn
	c.obj["output_json"] = globalOpts["json"] == true

	if len(rest) == 0 {
		// click no_args_is_help on the root group.
		c.echo(cliMainHelp())
		return 0
	}

	groupName := rest[0]
	var group *cliGroup
	var topCmd *cliCommand
	for _, e := range cliRegistry() {
		if e.group != nil && e.group.name == groupName {
			group = e.group
			break
		}
		if e.cmd != nil && e.cmd.name == groupName {
			topCmd = e.cmd
			break
		}
	}
	if group == nil && topCmd == nil {
		c.echoErr(fmt.Sprintf("Error: No such command '%s'.", groupName))
		return 2
	}

	// Bare top-level command (e.g. recall): no subcommand level.
	if topCmd != nil {
		cmdTokens := rest[1:]
		if cliHasHelpToken(cmdTokens) {
			c.echo(cliCommandHelpNamed(topCmd.name, topCmd))
			return 0
		}
		return cliRunCommand(c, topCmd, cmdTokens)
	}

	remaining := rest[1:]
	if len(remaining) == 0 {
		c.echo(cliGroupHelp(group))
		return 0
	}
	if remaining[0] == "--help" || remaining[0] == "-h" {
		c.echo(cliGroupHelp(group))
		return 0
	}

	cmdName := remaining[0]
	var cmd *cliCommand
	for _, cmd2 := range group.commands {
		if cmd2.name == cmdName {
			cmd = cmd2
			break
		}
	}
	if cmd == nil {
		c.echoErr(fmt.Sprintf("Error: No such command '%s'.", cmdName))
		return 2
	}
	cmdTokens := remaining[1:]
	if cliHasHelpToken(cmdTokens) {
		c.echo(cliCommandHelp(group, cmd))
		return 0
	}

	return cliRunCommand(c, cmd, cmdTokens)
}

// errCLIExit1 mirrors ctx.exit(1) after a JSON error envelope was printed.
var errCLIExit1 = &cliClickException{msg: ""}

// errCLIExit2 mirrors a bare sys.exit(2) (silent, e.g. promote --dry-run).
var errCLIExit2 = &cliUsageError{msg: ""}

// cliExitCodeError mirrors sys.exit(<arbitrary code>) (e.g. page edit
// re-raising the editor's exit status).
type cliExitCodeError struct{ code int }

func (e *cliExitCodeError) Error() string { return "" }

// cliPyBool renders a bool the way Python str() does.
func cliPyBool(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func asCLIUsageError(err error, target **cliUsageError) bool {
	if ue, ok := err.(*cliUsageError); ok {
		*target = ue
		return true
	}
	return false
}

func asCLIClickException(err error, target **cliClickException) bool {
	if ce, ok := err.(*cliClickException); ok {
		*target = ce
		return true
	}
	return false
}

func cliMainHelp() string {
	main := cliMainSpec()
	var b strings.Builder
	b.WriteString("Usage: octop-memory [OPTIONS] COMMAND [ARGS]...\n\n")
	b.WriteString("  " + main.help + "\n\nOptions:\n")
	for i := range main.options {
		o := &main.options[i]
		fmt.Fprintf(&b, "%s  %s\n", cliOptionUsage(o), o.help)
	}
	b.WriteString("  --version  Show the version and exit.\n")
	b.WriteString("  --help, -h  Show this message and exit.\n\nCommands:\n")
	for _, e := range cliRegistry() {
		if e.group != nil {
			fmt.Fprintf(&b, "  %s  %s\n", e.group.name, e.group.help)
		} else {
			fmt.Fprintf(&b, "  %s  %s\n", e.cmd.name, e.cmd.help)
		}
	}
	return b.String()
}
