package memory

// journal CLI command group, ported from adapters/cli/journal_cmd.py (L4
// audit log, read-only).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// cliJournalActionChoices mirrors get_args(JournalAction) order.
var cliJournalActionChoices = []string{
	"create", "extract_run", "promote", "update", "merge", "conflict",
	"deprecate", "delete", "user_edit", "reject", "page_regen", "page_regen_failed",
	"page_user_edit", "gc_rejected_candidate", "gc_deprecated_atom",
	"gc_orphan_raw_event", "consolidate", "entity_merge", "migration_in",
}

// cliJournalNotePreviewLen is the max journal note shown in the table view.
const cliJournalNotePreviewLen = 60

func cliJournalDict(e *JournalEntry) []cliPair {
	return ord(
		cliPair{"id", e.ID},
		cliPair{"timestamp", pyISOFormat(e.Timestamp)},
		cliPair{"action", string(e.Action)},
		cliPair{"actor", string(e.Actor)},
		cliPair{"target_entity_id", cliStrPtr(e.TargetEntityID)},
		cliPair{"target_atom_id", cliStrPtr(e.TargetAtomID)},
		cliPair{"target_candidate_id", cliStrPtr(e.TargetCandidateID)},
		cliPair{"before", cliMetaDict(e.Before)},
		cliPair{"after", cliMetaDict(e.After)},
		cliPair{"note", e.Note},
	)
}

// cliJournalParseSince ports journal_cmd._parse_since (messages differ from
// candidate_cmd's variant).
func cliJournalParseSince(spec string) (*time.Time, error) {
	spec = strings.TrimSpace(spec)
	runes := []rune(spec)
	if len(runes) > 0 && unicode.IsLetter(runes[len(runes)-1]) {
		unit := unicode.ToLower(runes[len(runes)-1])
		value, err := strconv.Atoi(string(runes[:len(runes)-1]))
		if err != nil {
			return nil, &cliUsageError{fmt.Sprintf("--since: invalid: %s", pyReprScalar(spec))}
		}
		var delta time.Duration
		switch unit {
		case 'm':
			delta = time.Duration(value) * time.Minute
		case 'h':
			delta = time.Duration(value) * time.Hour
		case 'd':
			delta = time.Duration(value) * 24 * time.Hour
		default:
			return nil, &cliUsageError{fmt.Sprintf(
				"--since: unsupported unit %s (use m / h / d)", pyReprScalar(string(unit)))}
		}
		t := time.Now().Add(-delta)
		return &t, nil
	}
	t, err := parseFlexibleISO(spec)
	if err != nil {
		return nil, &cliUsageError{fmt.Sprintf(
			"--since: invalid ISO 8601 datetime: %s", pyReprScalar(spec))}
	}
	return &t, nil
}

func cliJournalGroup() *cliGroup {
	return &cliGroup{
		name: "journal",
		help: "Inspect L4 audit log (append-only).",
		commands: []*cliCommand{
			{
				name: "list",
				help: "List journal entries (oldest-first within the window).",
				options: []cliOption{
					{name: "action", choices: cliJournalActionChoices, help: "Filter to one action type."},
					{name: "target-entity"},
					{name: "target-atom"},
					{name: "target-candidate"},
					{name: "since", help: "ISO 8601 datetime or short duration like '24h' / '7d' / '30m'."},
					{name: "limit", def: 100, isInt: true},
				},
				run: cliJournalList,
			},
		},
	}
}

func cliJournalList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	filter := JournalFilter{Limit: opts["limit"].(int)}
	if v := cliOptStr(opts, "action"); v != "" {
		a := JournalAction(v)
		filter.Action = &a
	}
	if v := cliOptStr(opts, "target-entity"); v != "" {
		filter.TargetEntityID = &v
	}
	if v := cliOptStr(opts, "target-atom"); v != "" {
		filter.TargetAtomID = &v
	}
	if v := cliOptStr(opts, "target-candidate"); v != "" {
		filter.TargetCandidateID = &v
	}
	if v := cliOptStr(opts, "since"); v != "" {
		after, err := cliJournalParseSince(v)
		if err != nil {
			return err
		}
		filter.After = after
	}
	entries, err := m.ListJournal(context.Background(), filter)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(entries))
		for _, e := range entries {
			dicts = append(dicts, cliJournalDict(e))
		}
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(entries) == 0 {
		c.echo("No journal entries found.")
		return nil
	}
	for _, e := range entries {
		cand := "-"
		if e.TargetCandidateID != nil {
			cand = cliRunePrefix(*e.TargetCandidateID, 8)
		}
		atom := "-"
		if e.TargetAtomID != nil {
			atom = cliRunePrefix(*e.TargetAtomID, 8)
		}
		ent := "-"
		if e.TargetEntityID != nil {
			ent = cliRunePrefix(*e.TargetEntityID, 8)
		}
		note := e.Note
		if len([]rune(note)) > cliJournalNotePreviewLen {
			note = cliRunePrefix(note, cliJournalNotePreviewLen) + "…"
		}
		c.echo(fmt.Sprintf("%s  %-10s by %-5s  cand=%s atom=%s ent=%s  %s",
			pyISOFormat(e.Timestamp), e.Action, e.Actor, cand, atom, ent, note))
	}
	return nil
}
