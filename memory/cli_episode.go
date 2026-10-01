package memory

// episode and digest CLI command groups, ported from
// adapters/cli/episode_cmd.py (M5 user-diary layer + daily/weekly/monthly
// digests).

import (
	"time"
	"context"
	"fmt"
	"strings"
)

// cliEpisodeDict is the 9-key list payload row.
func cliEpisodeDict(ep *Episode) []cliPair {
	var sessionID any
	if ep.SessionID != nil {
		sessionID = *ep.SessionID
	}
	return ord(
		cliPair{"id", ep.ID},
		cliPair{"occurred_at", pyISOFormat(ep.OccurredAt)},
		cliPair{"summary", ep.Summary},
		cliPair{"verbatim_quote", ep.VerbatimQuote},
		cliPair{"emotion", string(ep.Emotion)},
		cliPair{"intensity", ep.Intensity},
		cliPair{"people", cliStringSlice(ep.People)},
		cliPair{"topics", cliStringSlice(ep.Topics)},
		cliPair{"session_id", sessionID},
	)
}

func cliEpisodeGroup() *cliGroup {
	return &cliGroup{
		name: "episode",
		help: "Inspect and manage user-diary Episodes (M5).",
		commands: []*cliCommand{
			{
				name: "list",
				help: "List recent episodes ordered by occurred_at DESC.",
				options: []cliOption{
					{name: "limit", def: 20, isInt: true},
					{name: "emotion", help: "Filter by emotion (happy/sad/...)."},
					{name: "session"},
				},
				run: cliEpisodeList,
			},
			{
				name: "get",
				help: "Show one episode in full.",
				args: []cliArg{{name: "EPISODE_ID"}},
				run:  cliEpisodeGet,
			},
			{
				name: "search",
				help: "FTS search over episodes (summary + quote + people + topics).",
				args: []cliArg{{name: "QUERY"}},
				options: []cliOption{
					{name: "limit", def: 10, isInt: true},
				},
				run: cliEpisodeSearch,
			},
		},
	}
}

// cliEpisodeWhen renders occurred_at the way the Python CLI does:
// astimezone(UTC).strftime("%Y-%m-%d %H:%M").
func cliEpisodeWhen(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") }

func cliEpisodeList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	limit := opts["limit"].(int)
	f := EpisodeFilter{Limit: limit}
	if session := cliOptStr(opts, "session"); session != "" {
		f.SessionID = &session
	}
	if emotion := cliOptStr(opts, "emotion"); emotion != "" {
		f.Emotion = &emotion
	}
	episodes, err := m.ListEpisodes(context.Background(), f)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		payload := make([]any, 0, len(episodes))
		for _, ep := range episodes {
			payload = append(payload, cliEpisodeDict(ep))
		}
		c.echo(pyJSON(payload, false, "  "))
		return nil
	}
	if len(episodes) == 0 {
		c.echo("No episodes found.")
		return nil
	}
	for _, ep := range episodes {
		when := cliEpisodeWhen(ep.OccurredAt)
		people := ""
		if len(ep.People) > 0 {
			people = fmt.Sprintf(" 👥%s", strings.Join(ep.People, ","))
		}
		topics := ""
		if len(ep.Topics) > 0 {
			topics = fmt.Sprintf(" #%s", strings.Join(ep.Topics, ","))
		}
		c.echo(fmt.Sprintf("[%s] %s(%d)%s%s\n    %s\n    > %s\n",
			when, ep.Emotion, ep.Intensity, people, topics, ep.Summary, ep.VerbatimQuote))
	}
	return nil
}

func cliEpisodeGet(c *cliContext, opts map[string]any, args []string) error {
	episodeID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	ep, err := m.GetEpisode(context.Background(), episodeID)
	if err != nil {
		return cliFailf("%v", err)
	}
	if ep == nil {
		c.echoErr(fmt.Sprintf("Episode %s not found.", pyReprScalar(episodeID)))
		return errCLIExit1
	}
	c.echo(pyJSON(ord(
		cliPair{"id", ep.ID},
		cliPair{"occurred_at", pyISOFormat(ep.OccurredAt)},
		cliPair{"summary", ep.Summary},
		cliPair{"verbatim_quote", ep.VerbatimQuote},
		cliPair{"quote_event_id", ep.QuoteEventID},
		cliPair{"raw_event_ids", cliStringSlice(ep.RawEventIDs)},
		cliPair{"emotion", string(ep.Emotion)},
		cliPair{"intensity", ep.Intensity},
		cliPair{"people", cliStringSlice(ep.People)},
		cliPair{"topics", cliStringSlice(ep.Topics)},
		cliPair{"session_id", cliJSONStr(ep.SessionID)},
		cliPair{"extractor_version", ep.ExtractorVersion},
		cliPair{"created_at", pyISOFormat(ep.CreatedAt)},
	), false, "  "))
	return nil
}

func cliEpisodeSearch(c *cliContext, opts map[string]any, args []string) error {
	query := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	limit := opts["limit"].(int)
	episodes, err := m.SearchEpisodes(context.Background(), query, limit)
	if err != nil {
		return cliFailf("%v", err)
	}
	if len(episodes) == 0 {
		c.echo("No matching episodes.")
		return nil
	}
	for _, ep := range episodes {
		when := cliEpisodeWhen(ep.OccurredAt)
		c.echo(fmt.Sprintf("[%s] %s(%d) %s", when, ep.Emotion, ep.Intensity, ep.Summary))
	}
	return nil
}

// ---------------------------------------------------------------------------
// digest
// ---------------------------------------------------------------------------

func cliDigestGroup() *cliGroup {
	return &cliGroup{
		name: "digest",
		help: "Generate and inspect daily/weekly/monthly Episode digests.",
		commands: []*cliCommand{
			{
				name: "generate",
				help: "Generate (or regenerate) a daily/weekly/monthly digest.",
				options: []cliOption{
					{name: "kind", def: "daily", choices: []string{"daily", "weekly", "monthly"}},
					{name: "key", help: "Period key (e.g. 2026-06-26, 2026-W26, or 2026-06)."},
					{name: "out", help: "Directory to write the markdown file (default: don't write file)."},
					{name: "use-llm", offName: "no-llm", flag: true, def: true},
				},
				run: cliDigestGenerate,
			},
			{
				name: "list",
				help: "List existing digests, newest first.",
				options: []cliOption{
					{name: "kind", choices: []string{"daily", "weekly", "monthly"}},
					{name: "limit", def: 20, isInt: true},
				},
				run: cliDigestList,
			},
			{
				name: "show",
				help: "Print the markdown of one digest.",
				args: []cliArg{{name: "PERIOD_KEY"}},
				options: []cliOption{
					{name: "kind", def: "daily", choices: []string{"daily", "weekly", "monthly"}},
				},
				run: cliDigestShow,
			},
		},
	}
}

func cliDigestGenerate(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	var llm LLMClient
	if cliOptBool(opts, "use-llm") {
		// Same config helper the extractor uses; missing/broken config
		// degrades to the deterministic digest.
		cfg := LoadCLIConfig()
		if llmCfg, ok := cfg["llm"].(map[string]any); ok {
			llm = buildLLMClient(llmCfg)
		}
	}

	periodKind := DigestPeriod(cliOptStr(opts, "kind"))
	outDir := cliOptStr(opts, "out")
	if outDir != "" {
		if err := cliClickPathDir("--out", outDir); err != nil {
			return err
		}
		outDir = expandHomePath(outDir)
	}

	result, err := GenerateDigest(context.Background(), m, periodKind, DigestOptions{
		PeriodKey: cliOptStr(opts, "key"),
		LLM:       llm,
		OutputDir: outDir,
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	var filePath any
	if result.FilePath != "" {
		filePath = result.FilePath
	}
	if c.optBool("output_json") {
		c.echo(pyJSON(ord(
			cliPair{"period_kind", string(result.Digest.PeriodKind)},
			cliPair{"period_key", result.Digest.PeriodKey},
			cliPair{"episode_count", result.EpisodeCount},
			cliPair{"used_llm", result.UsedLLM},
			cliPair{"file_path", filePath},
		), false, "  "))
		return nil
	}
	c.echo(fmt.Sprintf("Digest generated: {'period_kind': '%s', 'period_key': '%s', 'episode_count': %d, 'used_llm': %s, 'file_path': %s}",
		result.Digest.PeriodKind, result.Digest.PeriodKey, result.EpisodeCount,
		cliPyBool(result.UsedLLM), pythonQuoteOpt(result.FilePath)))
	c.echo(strings.Repeat("─", 60))
	c.echo(result.Digest.Markdown)
	return nil
}

// pythonQuoteOpt renders str(path) the way Python repr would inside an
// f-string dict dump; empty → None.
func pythonQuoteOpt(s string) string {
	if s == "" {
		return "None"
	}
	return pyReprScalar(s)
}

func cliDigestList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	limit := opts["limit"].(int)
	var periodKind *DigestPeriod
	if kind := cliOptStr(opts, "kind"); kind != "" {
		pk := DigestPeriod(kind)
		periodKind = &pk
	}
	digests, err := m.ListDigests(context.Background(), periodKind, limit)
	if err != nil {
		return cliFailf("%v", err)
	}
	if len(digests) == 0 {
		c.echo("No digests yet.")
		return nil
	}
	for _, d := range digests {
		c.echo(fmt.Sprintf("%-7s %-10s episodes=%3d updated=%s",
			d.PeriodKind, d.PeriodKey, len(d.EpisodeIDs), pyISOFormat(d.UpdatedAt)))
	}
	return nil
}

func cliDigestShow(c *cliContext, opts map[string]any, args []string) error {
	periodKey := args[0]
	periodKind := DigestPeriod(cliOptStr(opts, "kind"))
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	digest, err := m.GetDigest(context.Background(), periodKind, periodKey)
	if err != nil {
		return cliFailf("%v", err)
	}
	if digest == nil {
		c.echoErr(fmt.Sprintf("Digest %s %s not found.", periodKind, pyReprScalar(periodKey)))
		return errCLIExit1
	}
	c.echo(digest.Markdown)
	return nil
}

