package memory

// atom CLI command group, ported from adapters/cli/atom_cmd.py (L2 AtomCards,
// read-only inspection).

import (
	"context"
	"fmt"
)

func cliAtomDict(a *AtomCard) []cliPair {
	var deprecatedAt any
	if a.DeprecatedAt != nil {
		deprecatedAt = pyISOFormat(*a.DeprecatedAt)
	}
	return ord(
		cliPair{"id", a.ID},
		cliPair{"entity_id", a.EntityID},
		cliPair{"candidate_id", a.CandidateID},
		cliPair{"raw_event_ids", cliStringSlice(a.RawEventIDs)},
		cliPair{"assertion", a.Assertion},
		cliPair{"verbatim_quote", a.VerbatimQuote},
		cliPair{"quote_event_id", a.QuoteEventID},
		cliPair{"search_terms", cliStringSlice(a.SearchTerms)},
		cliPair{"occurred_at", pyISOFormat(a.OccurredAt)},
		cliPair{"confidence", string(a.Confidence)},
		cliPair{"importance", string(a.Importance)},
		cliPair{"created_at", pyISOFormat(a.CreatedAt)},
		cliPair{"superseded_by", cliStrPtr(a.SupersededBy)},
		cliPair{"deprecated_at", deprecatedAt},
	)
}

// cliStringSlice renders a []string; nil becomes an empty list (Python
// dataclass list fields default to empty lists, never null).
func cliStringSlice(s []string) any {
	if s == nil {
		return []any{}
	}
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// cliRunePrefix returns the first n runes (Python s[:n] on str).
func cliRunePrefix(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func cliAtomGroup() *cliGroup {
	return &cliGroup{
		name: "atom",
		help: "Inspect L2 atoms (promoted candidates).",
		commands: []*cliCommand{
			{
				name: "list",
				help: "List atoms (most recently created first via list_atoms ordering).",
				options: []cliOption{
					{name: "entity-id", help: "Filter atoms hanging off this entity."},
					{name: "importance", choices: []string{"low", "medium", "high"}, help: "Filter by importance level."},
					{name: "include-deprecated", flag: true, def: false, help: "Show atoms that have been superseded."},
					{name: "limit", def: 50, isInt: true},
				},
				run: cliAtomList,
			},
			{
				name: "show",
				help: "Show full details of an atom by id (full UUID required).",
				args: []cliArg{{name: "ATOM_ID"}},
				run:  cliAtomShow,
			},
			{
				name: "search",
				help: "FTS5 search over atom assertion + verbatim_quote + search_terms.",
				args: []cliArg{{name: "QUERY"}},
				options: []cliOption{
					{name: "limit", def: 10, isInt: true},
					{name: "include-deprecated", flag: true, def: false},
				},
				run: cliAtomSearch,
			},
		},
	}
}

func cliAtomList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	filter := AtomFilter{Limit: opts["limit"].(int)}
	if v, _ := opts["entity-id"].(string); v != "" {
		filter.EntityID = &v
	}
	if v, _ := opts["importance"].(string); v != "" {
		imp := ImportanceLevel(v)
		filter.Importance = &imp
	}
	if b, _ := opts["include-deprecated"].(bool); b {
		filter.IncludeDeprecated = true
	}
	atoms, err := m.ListAtoms(context.Background(), filter)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(atoms))
		for _, a := range atoms {
			dicts = append(dicts, cliAtomDict(a))
		}
		// Bare list with indent=2, ensure_ascii=False.
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(atoms) == 0 {
		c.echo("No atoms found.")
		return nil
	}
	for _, a := range atoms {
		flag := "    "
		if a.DeprecatedAt != nil {
			flag = "DEPR"
		}
		c.echo(fmt.Sprintf("%s  %s  [%-6s/%-6s]  entity=%s  %s",
			cliRunePrefix(a.ID, 8), flag, a.Importance, a.Confidence,
			cliRunePrefix(a.EntityID, 8), cliRunePrefix(a.Assertion, 80)))
	}
	return nil
}

func cliAtomShow(c *cliContext, opts map[string]any, args []string) error {
	atomID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	a, err := m.GetAtom(context.Background(), atomID)
	if err != nil || a == nil {
		// sys.exit(1) — stderr message only.
		c.echoErr(fmt.Sprintf("Atom %s not found.", atomID))
		return errCLIExit1
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(cliAtomDict(a), false, "  "))
		return nil
	}

	c.echo(fmt.Sprintf("id            : %s", a.ID))
	c.echo(fmt.Sprintf("entity_id     : %s", a.EntityID))
	c.echo(fmt.Sprintf("candidate_id  : %s", a.CandidateID))
	c.echo(fmt.Sprintf("importance    : %s", a.Importance))
	c.echo(fmt.Sprintf("confidence    : %s", a.Confidence))
	c.echo(fmt.Sprintf("created_at    : %s", pyISOFormat(a.CreatedAt)))
	c.echo(fmt.Sprintf("occurred_at   : %s", pyISOFormat(a.OccurredAt)))
	if a.DeprecatedAt != nil {
		c.echo(fmt.Sprintf("deprecated_at : %s", pyISOFormat(*a.DeprecatedAt)))
		superseded := "-"
		if a.SupersededBy != nil {
			superseded = *a.SupersededBy
		}
		c.echo(fmt.Sprintf("superseded_by : %s", superseded))
	}
	c.echo(fmt.Sprintf("raw_event_ids : %s", pyJSON(cliStringSlice(a.RawEventIDs), true, "")))
	c.echo(fmt.Sprintf("quote_event   : %s", a.QuoteEventID))
	c.echo(fmt.Sprintf("search_terms  : %s", pyJSON(cliStringSlice(a.SearchTerms), true, "")))
	c.echo("")
	c.echo("assertion:")
	c.echo(fmt.Sprintf("  %s", a.Assertion))
	c.echo("verbatim_quote:")
	c.echo(fmt.Sprintf("  %s", a.VerbatimQuote))
	return nil
}

func cliAtomSearch(c *cliContext, opts map[string]any, args []string) error {
	query := args[0]
	limit, _ := opts["limit"].(int)
	includeDeprecated, _ := opts["include-deprecated"].(bool)

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	atoms, err := m.SearchAtoms(context.Background(), query, includeDeprecated, limit)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(atoms))
		for _, a := range atoms {
			dicts = append(dicts, cliAtomDict(a))
		}
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(atoms) == 0 {
		c.echo(fmt.Sprintf("No atoms matched %s.", pyReprScalar(query)))
		return nil
	}
	for _, a := range atoms {
		c.echo(fmt.Sprintf("%s  [%s]  entity=%s  %s",
			cliRunePrefix(a.ID, 8), a.Importance,
			cliRunePrefix(a.EntityID, 8), cliRunePrefix(a.Assertion, 80)))
	}
	return nil
}
