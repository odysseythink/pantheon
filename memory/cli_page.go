package memory

// page CLI command group, ported from adapters/cli/page_cmd.py (L3 entity
// pages: show / list-dirty / regen / edit).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

func cliPageDict(p *EntityPage) []cliPair {
	var lastRegenAt, lastUserEditAt any
	if p.LastRegenAt != nil {
		lastRegenAt = pyISOFormat(*p.LastRegenAt)
	}
	if p.LastUserEditAt != nil {
		lastUserEditAt = pyISOFormat(*p.LastUserEditAt)
	}
	return ord(
		cliPair{"id", p.ID},
		cliPair{"entity_id", p.EntityID},
		cliPair{"headline", p.Headline},
		cliPair{"topics", cliStringSlice(p.Topics)},
		cliPair{"dirty", p.Dirty},
		cliPair{"summary_version", p.SummaryVersion},
		cliPair{"regen_attempt_count", p.RegenAttemptCount},
		cliPair{"last_regen_at", lastRegenAt},
		cliPair{"last_user_edit_at", lastUserEditAt},
		cliPair{"summary_markdown", p.SummaryMarkdown},
	)
}

// cliPageRegenResultDict ports _result_to_dict.
func cliPageRegenResultDict(r *PageRegenResult) []cliPair {
	return ord(
		cliPair{"entity_id", r.EntityID},
		cliPair{"success", r.Success},
		cliPair{"reason", r.Reason},
		cliPair{"headline", r.Headline},
		cliPair{"topics", cliStringSlice(r.Topics)},
		cliPair{"summary_markdown", r.SummaryMarkdown},
		cliPair{"llm_calls", r.LLMCalls},
	)
}

func cliPageGroup() *cliGroup {
	return &cliGroup{
		name: "page",
		help: "Inspect and regenerate L3 entity pages.",
		commands: []*cliCommand{
			{
				name: "show",
				help: "Show the entity page (markdown + meta).",
				args: []cliArg{{name: "ENTITY_ID"}},
				run:  cliPageShow,
			},
			{
				name: "list-dirty",
				help: "List pages awaiting regeneration.",
				options: []cliOption{
					{name: "limit", def: 50, isInt: true},
				},
				run: cliPageListDirty,
			},
			{
				name: "regen",
				help: "Drive the LLM page regenerator (D33-B async cron entry point).",
				args: []cliArg{{name: "ENTITY_ID", optional: true}},
				options: []cliOption{
					{name: "dirty", flag: true, def: false, help: "Regenerate all dirty pages instead of one."},
					{name: "limit", def: 20, isInt: true, help: "Max pages to regen when --dirty is set."},
					{name: "dev-llm", help: "Dev-only LLM client for the regen step. Format: 'ollama:<model>' or 'remote:<base_url>:<model>'. Without this flag the CLI uses a no-op client (regen will record failure)."},
				},
				run: cliPageRegen,
			},
			{
				name: "edit",
				help: "Open the page summary in $EDITOR; save → apply user edit (D34-A).",
				args: []cliArg{{name: "ENTITY_ID"}},
				run:  cliPageEdit,
			},
		},
	}
}

func cliPageShow(c *cliContext, opts map[string]any, args []string) error {
	entityID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	page, err := m.GetEntityPage(ctx, entityID)
	if err != nil {
		return cliFailf("%v", err)
	}
	if page == nil {
		c.echoErr(fmt.Sprintf("No page for entity %s.", entityID))
		return errCLIExit1
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(cliPageDict(page), false, "  "))
		return nil
	}

	entity, err := m.GetEntity(ctx, entityID)
	if err != nil {
		return cliFailf("%v", err)
	}
	name := "(unknown entity)"
	if entity != nil {
		name = entity.CanonicalName
	}

	topics := "-"
	if len(page.Topics) > 0 {
		topics = strings.Join(page.Topics, ", ")
	}
	lastRegen := "-"
	if page.LastRegenAt != nil {
		lastRegen = pyISOFormat(*page.LastRegenAt)
	}
	lastEdit := "-"
	if page.LastUserEditAt != nil {
		lastEdit = pyISOFormat(*page.LastUserEditAt)
	}
	body := page.SummaryMarkdown
	if body == "" {
		body = "(empty)"
	}
	c.echo(fmt.Sprintf("entity         : %s  [%s]", name, entityID))
	c.echo(fmt.Sprintf("headline       : %s", page.Headline))
	c.echo(fmt.Sprintf("topics         : %s", topics))
	c.echo(fmt.Sprintf("dirty          : %s  (attempts=%d)", cliPyBool(page.Dirty), page.RegenAttemptCount))
	c.echo(fmt.Sprintf("summary_version: %d", page.SummaryVersion))
	c.echo(fmt.Sprintf("last_regen_at  : %s", lastRegen))
	c.echo(fmt.Sprintf("last_user_edit : %s", lastEdit))
	c.echo("")
	c.echo("--- summary_markdown ---")
	c.echo(body)
	return nil
}

func cliPageListDirty(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	pages, err := m.ListDirtyEntityPages(context.Background(), opts["limit"].(int))
	if err != nil {
		return cliFailf("%v", err)
	}
	if c.optBool("output_json") {
		dicts := make([]any, 0, len(pages))
		for _, p := range pages {
			dicts = append(dicts, cliPageDict(p))
		}
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(pages) == 0 {
		c.echo("No dirty pages.")
		return nil
	}
	for _, p := range pages {
		last := "never"
		if p.LastRegenAt != nil {
			last = pyISOFormat(*p.LastRegenAt)
		}
		c.echo(fmt.Sprintf("%s  v%d  attempts=%d  last_regen=%s  headline=%s",
			cliRunePrefix(p.EntityID, 8), p.SummaryVersion, p.RegenAttemptCount,
			last, pyReprScalar(cliRunePrefix(p.Headline, 30))))
	}
	return nil
}

func cliPageRegen(c *cliContext, opts map[string]any, args []string) error {
	allDirty, _ := opts["dirty"].(bool)
	entityID := ""
	if len(args) > 0 {
		entityID = args[0]
	}
	if !allDirty && entityID == "" {
		return &cliUsageError{"Provide ENTITY_ID or use --dirty to process all dirty pages."}
	}
	if allDirty && entityID != "" {
		return &cliUsageError{"--dirty and ENTITY_ID are mutually exclusive."}
	}

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	// _resolve_llm: no flag → NoopLLMClient (regen records failure).
	llm, err := cliParseDevLLM(cliOptStr(opts, "dev-llm"))
	if err != nil {
		return err
	}

	if allDirty {
		batch, err := RegenerateDirty(ctx, m, llm, opts["limit"].(int), DefaultMaxAtomsPerRegen)
		if err != nil {
			return cliFailf("%v", err)
		}
		if c.optBool("output_json") {
			results := make([]any, 0, len(batch.Results))
			llmCalls := 0
			for _, r := range batch.Results {
				results = append(results, cliPageRegenResultDict(r))
				llmCalls += r.LLMCalls
			}
			c.echo(pyJSON(ord(
				cliPair{"success_count", batch.SuccessCount()},
				cliPair{"failure_count", batch.FailureCount()},
				cliPair{"llm_calls", llmCalls},
				cliPair{"results", results},
			), false, "  "))
			return nil
		}
		c.echo(fmt.Sprintf("Processed %d pages: ok=%d fail=%d",
			len(batch.Results), batch.SuccessCount(), batch.FailureCount()))
		for _, r := range batch.Results {
			tag := "FAIL"
			if r.Success {
				tag = "ok "
			}
			c.echo(fmt.Sprintf("  [%s] %s  %s", tag, cliRunePrefix(r.EntityID, 8), r.Reason))
		}
		return nil
	}

	result, err := RegeneratePage(ctx, m, entityID, llm, PageRegenOptions{})
	if err != nil {
		return cliFailf("%v", err)
	}
	if c.optBool("output_json") {
		c.echo(pyJSON(cliPageRegenResultDict(result), false, "  "))
		return nil
	}
	c.echo(fmt.Sprintf("entity_id : %s", result.EntityID))
	c.echo(fmt.Sprintf("success   : %s", cliPyBool(result.Success)))
	c.echo(fmt.Sprintf("reason    : %s", result.Reason))
	if result.Success {
		c.echo(fmt.Sprintf("headline  : %s", result.Headline))
		c.echo(fmt.Sprintf("topics    : %s", strings.Join(result.Topics, ", ")))
		c.echo("")
		c.echo("--- summary_markdown ---")
		c.echo(result.SummaryMarkdown)
	}
	return nil
}

func cliPageEdit(c *cliContext, opts map[string]any, args []string) error {
	entityID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	page, err := m.GetEntityPage(ctx, entityID)
	if err != nil {
		return cliFailf("%v", err)
	}
	if page == nil {
		c.echoErr(fmt.Sprintf("No page for entity %s; run `memory page regen %s` first.", entityID, entityID))
		return errCLIExit1
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	// Only keep the first token and confirm it is available in PATH
	// (mirrors shlex.split(editor)[0] + shutil.which).
	fields := strings.Fields(editor)
	editorCmd := ""
	if len(fields) > 0 {
		editorCmd = fields[0]
	}
	editorPath, err := exec.LookPath(editorCmd)
	if err != nil {
		c.echoErr(fmt.Sprintf("Editor %s not found in PATH; aborting.", pyReprScalar(editorCmd)))
		return errCLIExit1
	}

	tmp, err := os.CreateTemp("", "octop-page-*.md")
	if err != nil {
		return cliFailf("%v", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(page.SummaryMarkdown); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return cliFailf("%v", err)
	}
	tmp.Close()

	proc := exec.Command(editorPath, tmpPath)
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	runErr := proc.Run()
	if runErr != nil {
		os.Remove(tmpPath)
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			c.echoErr(fmt.Sprintf("Editor exited with status %d; not saving.", exitErr.ExitCode()))
			return &cliExitCodeError{code: exitErr.ExitCode()}
		}
		return cliFailf("%v", runErr)
	}

	newBodyBytes, readErr := os.ReadFile(tmpPath)
	os.Remove(tmpPath)
	if readErr != nil {
		return cliFailf("%v", readErr)
	}
	newBody := string(newBodyBytes)

	if newBody == page.SummaryMarkdown {
		c.echo("No changes.")
		return nil
	}

	ok, err := m.ApplyEntityPageUserEdit(ctx, entityID, newBody, nil)
	if err != nil {
		return cliFailf("%v", err)
	}
	if !ok {
		c.echoErr("Edit failed: page row missing.")
		return errCLIExit1
	}

	now := time.Now().UTC()
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID:             fmt.Sprintf("page_edit_%s_%s", entityID, pyISOFormat(now)),
		Timestamp:      now,
		Action:         JournalActionPageUserEdit,
		Actor:          DecidedByUser,
		TargetEntityID: &entityID,
		Note:           "user edit applied via `memory page edit`",
	}); err != nil {
		return cliFailf("%v", err)
	}
	c.echo(fmt.Sprintf("Saved %d chars; summary_version bumped.", utf8.RuneCountInString(newBody)))
	return nil
}
