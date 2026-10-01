// Package memory — operations.migration port (namespace export / import /
// rename / preview / backfill).
//
// Mirrors src/octop_memory/operations/migration/{__init__,export,import_,
// rename,preview,backfill}.py.
//
// The wire format is newline-delimited JSON envelopes:
//
//	{"v":1,"table":"__header__","data":{...}}
//	{"v":1,"table":"raw_events","data":{...}}
//	...
//	{"v":1,"table":"__footer__","data":{"counts":{...}}}
//
// The exporter streams table by table (never loads a whole namespace into
// memory); the importer replays envelopes through the same Memory API the
// live pipeline uses, so every insert gets the same FTS-trigger treatment.
package memory

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ExportVersion is the envelope version. Bumped on breaking wire changes;
// importers refuse anything else.
const ExportVersion = 1

// MigrationToolVersion is informational on the wire.
const MigrationToolVersion = "octop_memory.operations.migration/1"

// ExportTables is the canonical table order (dependency-safe): raw evidence
// first, audit log last.
var ExportTables = []string{
	"raw_events", "candidates", "entities", "atoms", "aliases",
	"entity_pages", "thread_active_entities", "journal",
}

// unlimitedRows mirrors Python's limit=10**9 "everything" bound.
const unlimitedRows = 1_000_000_000

// ---------------------------------------------------------------------------
// Export (export.py)
// ---------------------------------------------------------------------------

// ExportSummary is the outcome of one ExportNamespace call.
type ExportSummary struct {
	Namespace  string         `json:"namespace"`
	OutPath    string         `json:"out_path"`
	Counts     map[string]int `json:"counts"`
	ToolVersion string        `json:"tool_version"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at"`
}

// TotalRows sums the per-table counts.
func (s *ExportSummary) TotalRows() int {
	total := 0
	for _, n := range s.Counts {
		total += n
	}
	return total
}

// ExportNamespace streams every row of the given tables to a JSONL file.
//
// gzipCompress forces gzip; a ".gz" suffix on outPath triggers it too
// (mirrors Python's _open_output). Unknown table names are an error — strict
// so typos don't silently produce empty dumps.
func ExportNamespace(ctx context.Context, m *Memory, outPath string, gzipCompress bool, tables ...string) (*ExportSummary, error) {
	if len(tables) == 0 {
		tables = ExportTables
	}
	for _, t := range tables {
		if !containsStr(ExportTables, t) {
			return nil, fmt.Errorf("unknown export table: %q", t)
		}
	}
	out := expandHomePath(outPath)
	if dir := filepath.Dir(out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("migration: create output dir: %w", err)
		}
	}

	summary := &ExportSummary{
		Namespace:   m.Namespace(),
		OutPath:     out,
		Counts:      map[string]int{},
		ToolVersion: MigrationToolVersion,
		StartedAt:   time.Now().UTC(),
	}
	for _, t := range tables {
		summary.Counts[t] = 0
	}

	file, err := os.Create(out)
	if err != nil {
		return nil, fmt.Errorf("migration: open output: %w", err)
	}
	writeErr := func() error {
		var fp io.Writer = file
		if gzipCompress || strings.HasSuffix(out, ".gz") {
			gz := gzip.NewWriter(file)
			defer gz.Close()
			fp = gz
		}
		enc := json.NewEncoder(fp)
		enc.SetEscapeHTML(false) // Python: ensure_ascii=False

		writeRecord := func(table string, data any) error {
			return enc.Encode(map[string]any{"v": ExportVersion, "table": table, "data": data})
		}

		// Header first so partial writes are still self-identifying.
		if err := writeRecord("__header__", map[string]any{
			"version":      ExportVersion,
			"tool_version": MigrationToolVersion,
			"namespace":    m.Namespace(),
			"created_at":   summary.StartedAt.Format(time.RFC3339Nano),
			"tables":       tables,
		}); err != nil {
			return err
		}

		for _, table := range tables {
			rows, err := iterTableRows(ctx, m, table)
			if err != nil {
				return err
			}
			for _, row := range rows {
				if err := writeRecord(table, row); err != nil {
					return err
				}
				summary.Counts[table]++
			}
		}

		finished := time.Now().UTC()
		summary.FinishedAt = &finished
		return writeRecord("__footer__", map[string]any{
			"finished_at": finished.Format(time.RFC3339Nano),
			"counts":      summary.Counts,
			"total_rows":  summary.TotalRows(),
		})
	}()
	if err := file.Close(); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return nil, writeErr
	}
	return summary, nil
}

// iterTableRows gathers serialisable rows for one export table. The Python
// exporter streams lazily; gathering a slice keeps the Go code simple while
// the API surface (Memory methods only, no raw SQL except the per-thread
// active-entity read-through) stays identical.
func iterTableRows(ctx context.Context, m *Memory, table string) ([]any, error) {
	switch table {
	case "raw_events":
		rows, err := m.ListRaw(ctx, RawEventFilter{Limit: unlimitedRows})
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil

	case "candidates":
		rows, err := m.ListCandidates(ctx, CandidateFilter{Limit: unlimitedRows})
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil

	case "atoms":
		rows, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: unlimitedRows})
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil

	case "entities":
		rows, err := m.ListEntities(ctx, nil, unlimitedRows)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil

	case "aliases":
		rows, err := m.ListAliases(ctx, nil, unlimitedRows)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil

	case "entity_pages":
		// Pages are entity-keyed; iterating entities → get_entity_page is
		// the canonical traversal (no list_pages API, same as Python).
		entities, err := m.ListEntities(ctx, nil, unlimitedRows)
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, e := range entities {
			page, err := m.GetEntityPage(ctx, e.ID)
			if err != nil {
				return nil, err
			}
			if page != nil {
				out = append(out, page)
			}
		}
		return out, nil

	case "thread_active_entities":
		// No global "list every thread" API — read through to the backend
		// table directly (SQLite only, same dispatch as Python).
		q := fmt.Sprintf(`SELECT thread_id, entity_id, last_seen_at, source
                FROM %s_thread_active_entities
                ORDER BY thread_id, last_seen_at DESC`, m.Backend().Namespace())
		dbRows, err := m.Backend().DB().QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer dbRows.Close()
		out := []any{}
		for dbRows.Next() {
			var threadID, entityID, lastSeen, source sql.NullString
			if err := dbRows.Scan(&threadID, &entityID, &lastSeen, &source); err != nil {
				return nil, err
			}
			ts, err := parseFlexibleISO(lastSeen.String)
			if err != nil {
				return nil, fmt.Errorf("migration: bad last_seen_at %q: %w", lastSeen.String, err)
			}
			src := source.String
			if src == "" {
				src = string(ActiveEntitySourceRecallHit)
			}
			out = append(out, &ActiveEntity{
				ThreadID:   threadID.String,
				EntityID:   entityID.String,
				LastSeenAt: ts,
				Source:     ActiveEntitySource(src),
			})
		}
		return out, dbRows.Err()

	case "journal":
		rows, err := m.ListJournal(ctx, JournalFilter{Limit: unlimitedRows})
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown export table: %q", table)
}

// ---------------------------------------------------------------------------
// Import (import_.py)
// ---------------------------------------------------------------------------

// OnConflict is the behaviour when an imported row's PK already exists.
type OnConflict string

const (
	OnConflictSkip    OnConflict = "skip"
	OnConflictReplace OnConflict = "replace" // behaves like skip today; full upsert lands later
	OnConflictRaise   OnConflict = "raise"
)

// ImportSummary is the outcome of one ImportNamespace call.
type ImportSummary struct {
	Applied map[string]int   `json:"applied"`
	Skipped map[string]int   `json:"skipped"`
	Errors  []string         `json:"errors"`
	Header  map[string]any   `json:"-"`
	Footer  map[string]any   `json:"-"`
}

// TotalApplied sums the per-table applied counts.
func (s *ImportSummary) TotalApplied() int {
	total := 0
	for _, n := range s.Applied {
		total += n
	}
	return total
}

// TotalSkipped sums the per-table skipped counts.
func (s *ImportSummary) TotalSkipped() int {
	total := 0
	for _, n := range s.Skipped {
		total += n
	}
	return total
}

// ImportOptions customizes ImportNamespace.
type ImportOptions struct {
	OnConflict      OnConflict // zero value → skip
	ExpectNamespace string     // refuse dumps whose header advertises another namespace
}

// errBadRow marks data-level problems the importer should record and skip
// (Python: ValueError caught in import_namespace).
var errBadRow = errors.New("bad row")

func badRowf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRow, fmt.Sprintf(format, args...))
}

// ImportNamespace replays a JSONL dump into m.
//
// Robustness matches Python: gzipped files (".gz" suffix), mid-file partial
// dumps, header-version mismatch → strict refuse, per-row data errors are
// recorded and skipped, PK conflicts follow OnConflict.
func ImportNamespace(ctx context.Context, m *Memory, inPath string, opts ImportOptions) (*ImportSummary, error) {
	summary := &ImportSummary{
		Applied: map[string]int{},
		Skipped: map[string]int{},
		Errors:  []string{},
		Header:  map[string]any{},
		Footer:  map[string]any{},
	}
	path := expandHomePath(inPath)

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("migration: open input: %w", err)
	}
	defer file.Close()

	var fp io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("migration: gzip: %w", err)
		}
		defer gz.Close()
		fp = gz
	}

	scanner := bufio.NewScanner(fp)
	scanner.Buffer(make([]byte, 256*1024), 64*1024*1024)
	lineno := 0
	for scanner.Scan() {
		lineno++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope struct {
			V     json.RawMessage `json:"v"`
			Table json.RawMessage `json:"table"`
			Data  json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			msg := err.Error()
			// Python json.JSONDecodeError exposes .msg only.
			if se, ok := err.(*json.SyntaxError); ok {
				msg = se.Error()
			}
			return nil, fmt.Errorf("line %d: invalid JSON: %s", lineno, msg)
		}
		version, versionPresent := jsonNumber(envelope.V)
		if !versionPresent || int(version) != ExportVersion {
			return nil, fmt.Errorf("line %d: incompatible export version v=%s; this build supports v=%d",
				lineno, jsonRepr(envelope.V), ExportVersion)
		}
		if len(envelope.Table) == 0 || string(envelope.Table) == "null" {
			return nil, fmt.Errorf("line %d: missing 'table' field", lineno)
		}
		var table string
		if err := json.Unmarshal(envelope.Table, &table); err != nil {
			return nil, fmt.Errorf("line %d: missing 'table' field", lineno)
		}

		if table == "__header__" {
			data := map[string]any{}
			_ = json.Unmarshal(envelope.Data, &data)
			summary.Header = data
			if opts.ExpectNamespace != "" && data["namespace"] != opts.ExpectNamespace {
				return nil, fmt.Errorf("namespace mismatch: dump says %s, expected %q",
					jsonReprValue(data["namespace"]), opts.ExpectNamespace)
			}
			continue
		}
		if table == "__footer__" {
			data := map[string]any{}
			_ = json.Unmarshal(envelope.Data, &data)
			summary.Footer = data
			continue
		}

		var data map[string]any
		if err := json.Unmarshal(envelope.Data, &data); err != nil || data == nil {
			data = nil
		}
		if data == nil {
			if len(envelope.Data) != 0 && string(envelope.Data) != "null" {
				// non-object data → Python: ValueError("{table}: data must be object")
				var probe any
				_ = json.Unmarshal(envelope.Data, &probe)
				summary.Errors = append(summary.Errors,
					fmt.Sprintf("line %d: %s: data must be object, got %s", lineno, table, jsonTypeName(probe)))
				continue
			}
			summary.Errors = append(summary.Errors,
				fmt.Sprintf("line %d: %s: data must be object, got NoneType", lineno, table))
			continue
		}

		applied, skipped, err := applyImportRow(ctx, m, table, data, opts.OnConflict)
		switch {
		case err == nil:
			// fallthrough to counters below
		case errors.Is(err, errBadRow):
			summary.Errors = append(summary.Errors, fmt.Sprintf("line %d: %s", lineno, stripBadRowPrefix(err)))
			continue
		case isUniqueConstraintErr(err):
			if opts.OnConflict == OnConflictRaise {
				return nil, fmt.Errorf("line %d: %w", lineno, err)
			}
			// skip and replace both surface as skipped today.
			summary.Skipped[table]++
			continue
		default:
			return nil, fmt.Errorf("line %d: %w", lineno, err)
		}
		if applied {
			summary.Applied[table]++
		}
		if skipped {
			summary.Skipped[table]++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("migration: read input: %w", err)
	}
	return summary, nil
}

// stripBadRowPrefix removes the "bad row: " sentinel text so the recorded
// error reads like Python's ValueError message.
func stripBadRowPrefix(err error) string {
	msg := err.Error()
	if idx := strings.Index(msg, ": "); idx >= 0 && strings.HasPrefix(msg, errBadRow.Error()) {
		return msg[idx+2:]
	}
	return msg
}

// jsonNumber parses a JSON number token; ok=false when missing or not numeric.
func jsonNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}

// jsonRepr renders a raw JSON token the way Python repr would for the
// version-mismatch message (numbers bare, null → None, strings quoted).
func jsonRepr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "None"
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" {
		return "None"
	}
	return s
}

// jsonReprValue renders an any the way Python repr would (strings get
// single quotes; nil → None).
func jsonReprValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return "'" + x + "'"
	default:
		return fmt.Sprintf("%v", x)
	}
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// isUniqueConstraintErr matches the SQLite UNIQUE/PK violation text surfaces
// by the modernc driver ("constraint failed: UNIQUE constraint failed: ...").
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY must be unique")
}

// ---------------------------------------------------------------------------
// Per-row constructors (import_.py _build_*)
// ---------------------------------------------------------------------------

// coerceStr mirrors Python str(): strings pass through; scalars stringify.
func coerceStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func envRequired(d map[string]any, key string) (string, error) {
	v, ok := d[key]
	if !ok {
		return "", badRowf("missing required field '%s'", key)
	}
	if v == nil {
		return "None", nil // Python str(None) on an explicit null
	}
	return coerceStr(v), nil
}

func envStrOr(d map[string]any, key, def string) string {
	v, ok := d[key]
	if !ok || v == nil {
		return def // Python: str(d.get(k) or def) — null/missing → def
	}
	return coerceStr(v)
}

func envStrPtr(d map[string]any, key string) *string {
	v, ok := d[key]
	if !ok || v == nil {
		return nil
	}
	s := coerceStr(v)
	return &s
}

func envTime(d map[string]any, key string) (time.Time, error) {
	raw, err := envRequired(d, key)
	if err != nil {
		return time.Time{}, err
	}
	ts, perr := parseFlexibleISO(raw)
	if perr != nil {
		return time.Time{}, badRowf("invalid timestamp for '%s': %s", key, raw)
	}
	return ts, nil
}

func envTimePtr(d map[string]any, key string) (*time.Time, error) {
	v, ok := d[key]
	if !ok || v == nil {
		return nil, nil
	}
	raw := coerceStr(v)
	ts, perr := parseFlexibleISO(raw)
	if perr != nil {
		return nil, badRowf("invalid timestamp for '%s': %s", key, raw)
	}
	return &ts, nil
}

func envStrList(d map[string]any, key string) []string {
	v, ok := d[key]
	if !ok || v == nil {
		return []string{}
	}
	list, isList := v.([]any)
	if !isList {
		return []string{}
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, coerceStr(item))
	}
	return out
}

func envIntOr(d map[string]any, key string, def int) int {
	v, ok := d[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case float64:
		return int(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
		return def
	default:
		return def
	}
}

// envJSONTruthy mirrors bool(value) for the "dirty" default-true corner:
// explicit null → False (Python bool(None)), missing → default.
func envJSONTruthy(d map[string]any, key string, def bool) bool {
	v, ok := d[key]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	default:
		return true
	}
}

func buildImportRawEvent(d map[string]any) (*RawEvent, error) {
	id, err := envRequired(d, "id")
	if err != nil {
		return nil, err
	}
	host, err := envRequired(d, "host")
	if err != nil {
		return nil, err
	}
	ts, err := envTime(d, "timestamp")
	if err != nil {
		return nil, err
	}
	etype, err := envRequired(d, "event_type")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if p, ok := d["payload"].(map[string]any); ok {
		payload = p
	} else if d["payload"] != nil {
		return nil, badRowf("payload must be object")
	}
	return &RawEvent{
		ID:        id,
		Host:      host,
		SessionID: envStrPtr(d, "session_id"),
		ThreadID:  envStrPtr(d, "thread_id"),
		User:      envStrPtr(d, "user"),
		Timestamp: ts,
		EventType: RawEventType(etype),
		Content:   envStrOr(d, "content", ""),
		Payload:   payload,
	}, nil
}

func buildImportCandidate(d map[string]any) (*Candidate, error) {
	id, err := envRequired(d, "id")
	if err != nil {
		return nil, err
	}
	ctype, err := envRequired(d, "candidate_type")
	if err != nil {
		return nil, err
	}
	createdAt, err := envTime(d, "created_at")
	if err != nil {
		return nil, err
	}
	payload := map[string]any{}
	if p, ok := d["payload"].(map[string]any); ok {
		payload = p
	}
	c := &Candidate{
		ID:                id,
		RawEventIDs:       envStrList(d, "raw_event_ids"),
		CandidateType:     CandidateType(ctype),
		Status:            CandidateStatus(envStrOr(d, "status", "pending")),
		Title:             envStrOr(d, "title", ""),
		Assertion:         envStrOr(d, "assertion", ""),
		VerbatimQuote:     envStrOr(d, "verbatim_quote", ""),
		QuoteEventID:      envStrOr(d, "quote_event_id", ""),
		SubjectName:       envStrOr(d, "subject_name", ""),
		SubjectEntityType: EntityType(envStrOr(d, "subject_entity_type", "Fact")),
		TargetEntityID:    envStrPtr(d, "target_entity_id"),
		Confidence:        ConfidenceLevel(envStrOr(d, "confidence", "medium")),
		Importance:        ImportanceLevel(envStrOr(d, "importance", "medium")),
		RecommendedAction: RecommendedAction(envStrOr(d, "recommended_action", "promote")),
		PromotionReason:   envStrOr(d, "promotion_reason", ""),
		ExtractorVersion:  envStrOr(d, "extractor_version", ""),
		CreatedAt:         createdAt,
		DecidedAt:         nil,
		SessionID:         envStrPtr(d, "session_id"),
		Payload:           payload,
	}
	if v, ok := d["decided_by"]; ok && v != nil {
		by := DecidedBy(coerceStr(v))
		c.DecidedBy = &by
	}
	if dt, err := envTimePtr(d, "decided_at"); err != nil {
		return nil, err
	} else if dt != nil {
		c.DecidedAt = dt
	}
	return c, nil
}

func buildImportAtom(d map[string]any) (*AtomCard, error) {
	req := func(key string) (string, error) { return envRequired(d, key) }
	id, err := req("id")
	if err != nil {
		return nil, err
	}
	entityID, err := req("entity_id")
	if err != nil {
		return nil, err
	}
	candidateID, err := req("candidate_id")
	if err != nil {
		return nil, err
	}
	assertion, err := req("assertion")
	if err != nil {
		return nil, err
	}
	verbatim, err := req("verbatim_quote")
	if err != nil {
		return nil, err
	}
	quoteEventID, err := req("quote_event_id")
	if err != nil {
		return nil, err
	}
	occurredAt, err := envTime(d, "occurred_at")
	if err != nil {
		return nil, err
	}
	createdAt, err := envTime(d, "created_at")
	if err != nil {
		return nil, err
	}
	a := &AtomCard{
		ID:            id,
		EntityID:      entityID,
		CandidateID:   candidateID,
		RawEventIDs:   envStrList(d, "raw_event_ids"),
		Assertion:     assertion,
		VerbatimQuote: verbatim,
		QuoteEventID:  quoteEventID,
		SearchTerms:   envStrList(d, "search_terms"),
		OccurredAt:    occurredAt,
		Confidence:    ConfidenceLevel(envStrOr(d, "confidence", "medium")),
		Importance:    ImportanceLevel(envStrOr(d, "importance", "medium")),
		CreatedAt:     createdAt,
	}
	a.SupersededBy = envStrPtr(d, "superseded_by")
	if dep, err := envTimePtr(d, "deprecated_at"); err != nil {
		return nil, err
	} else if dep != nil {
		a.DeprecatedAt = dep
	}
	return a, nil
}

func buildImportEntity(d map[string]any) (*Entity, error) {
	id, err := envRequired(d, "id")
	if err != nil {
		return nil, err
	}
	etype, err := envRequired(d, "entity_type")
	if err != nil {
		return nil, err
	}
	name, err := envRequired(d, "canonical_name")
	if err != nil {
		return nil, err
	}
	createdAt, err := envTime(d, "created_at")
	if err != nil {
		return nil, err
	}
	e := &Entity{
		ID:            id,
		EntityType:    EntityType(etype),
		CanonicalName: name,
		Aliases:       envStrList(d, "aliases"),
		AtomCount:     envIntOr(d, "atom_count", 0),
		CreatedAt:     createdAt,
	}
	if lp, err := envTimePtr(d, "last_promoted_at"); err != nil {
		return nil, err
	} else if lp != nil {
		e.LastPromotedAt = lp
	}
	return e, nil
}

var importAliasCreatedBy = map[string]bool{"auto": true, "user": true, "rule": true}

func buildImportAlias(d map[string]any) (*Alias, error) {
	alias, err := envRequired(d, "alias")
	if err != nil {
		return nil, err
	}
	entityID, err := envRequired(d, "entity_id")
	if err != nil {
		return nil, err
	}
	etype, err := envRequired(d, "entity_type")
	if err != nil {
		return nil, err
	}
	createdAt, err := envTime(d, "created_at")
	if err != nil {
		return nil, err
	}
	createdBy := envStrOr(d, "created_by", "auto")
	if !importAliasCreatedBy[createdBy] {
		createdBy = "auto"
	}
	return &Alias{
		Alias:      alias,
		EntityID:   entityID,
		EntityType: EntityType(etype),
		CreatedBy:  DecidedBy(createdBy),
		CreatedAt:  createdAt,
	}, nil
}

func buildImportEntityPage(d map[string]any) (*EntityPage, error) {
	id, err := envRequired(d, "id")
	if err != nil {
		return nil, err
	}
	entityID, err := envRequired(d, "entity_id")
	if err != nil {
		return nil, err
	}
	createdAt, err := envTime(d, "created_at")
	if err != nil {
		return nil, err
	}
	updatedAt, err := envTime(d, "updated_at")
	if err != nil {
		return nil, err
	}
	p := &EntityPage{
		ID:                id,
		EntityID:          entityID,
		SummaryMarkdown:   envStrOr(d, "summary_markdown", ""),
		Headline:          envStrOr(d, "headline", ""),
		Topics:            envStrList(d, "topics"),
		Dirty:             envJSONTruthy(d, "dirty", true),
		RegenAttemptCount: envIntOr(d, "regen_attempt_count", 0),
		SummaryVersion:    envIntOr(d, "summary_version", 0),
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
	}
	if lr, err := envTimePtr(d, "last_regen_at"); err != nil {
		return nil, err
	} else if lr != nil {
		p.LastRegenAt = lr
	}
	if lu, err := envTimePtr(d, "last_user_edit_at"); err != nil {
		return nil, err
	} else if lu != nil {
		p.LastUserEditAt = lu
	}
	return p, nil
}

func buildImportActiveEntity(d map[string]any) (*ActiveEntity, error) {
	threadID, err := envRequired(d, "thread_id")
	if err != nil {
		return nil, err
	}
	entityID, err := envRequired(d, "entity_id")
	if err != nil {
		return nil, err
	}
	lastSeen, err := envTime(d, "last_seen_at")
	if err != nil {
		return nil, err
	}
	return &ActiveEntity{
		ThreadID:   threadID,
		EntityID:   entityID,
		LastSeenAt: lastSeen,
		Source:     ActiveEntitySource(envStrOr(d, "source", "recall_hit")),
	}, nil
}

func buildImportJournal(d map[string]any) (*JournalEntry, error) {
	id, err := envRequired(d, "id")
	if err != nil {
		return nil, err
	}
	ts, err := envTime(d, "timestamp")
	if err != nil {
		return nil, err
	}
	action, err := envRequired(d, "action")
	if err != nil {
		return nil, err
	}
	e := &JournalEntry{
		ID:        id,
		Timestamp: ts,
		Action:    JournalAction(action),
		Actor:     DecidedBy(envStrOr(d, "actor", "auto")),
		Note:      envStrOr(d, "note", ""),
	}
	e.TargetEntityID = envStrPtr(d, "target_entity_id")
	e.TargetAtomID = envStrPtr(d, "target_atom_id")
	e.TargetCandidateID = envStrPtr(d, "target_candidate_id")
	if b, ok := d["before"].(map[string]any); ok {
		e.Before = b
	}
	if a, ok := d["after"].(map[string]any); ok {
		e.After = a
	}
	return e, nil
}

// applyImportRow writes one decoded row through the Memory API.
// Returns (applied, skipped, err). Conflicts surface as unique-constraint
// errors for the caller to arbitrate via OnConflict.
func applyImportRow(ctx context.Context, m *Memory, table string, d map[string]any, onConflict OnConflict) (bool, bool, error) {
	switch table {
	case "raw_events":
		e, err := buildImportRawEvent(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.AddRawBatch(ctx, []*RawEvent{e})

	case "candidates":
		c, err := buildImportCandidate(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.AddCandidate(ctx, c)

	case "atoms":
		a, err := buildImportAtom(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.AddAtom(ctx, a, nil)

	case "entities":
		e, err := buildImportEntity(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.AddEntity(ctx, e)

	case "aliases":
		a, err := buildImportAlias(d)
		if err != nil {
			return false, false, err
		}
		// SaveAlias is INSERT OR IGNORE — duplicates are invisible no-ops,
		// so aliases always count as applied (same as the Python backend).
		return true, false, m.AddAlias(ctx, a)

	case "entity_pages":
		// upsert-by-entity_id semantics — always replaces, on_conflict N/A.
		p, err := buildImportEntityPage(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.UpsertEntityPage(ctx, p)

	case "thread_active_entities":
		ae, err := buildImportActiveEntity(d)
		if err != nil {
			return false, false, err
		}
		// Don't auto-evict during import — preserve dump order (keep=10**9).
		when := ae.LastSeenAt
		return true, false, m.UpsertActiveEntity(ctx, ae.ThreadID, ae.EntityID, ae.Source, &when, math.MaxInt)

	case "journal":
		e, err := buildImportJournal(d)
		if err != nil {
			return false, false, err
		}
		return true, false, m.AppendJournal(ctx, e)
	}
	return false, false, badRowf("unknown import table: '%s'", table)
}

// ---------------------------------------------------------------------------
// Rename (rename.py)
// ---------------------------------------------------------------------------

// RenameMode selects the rename strategy: "rename" moves the tables (source
// namespace ceases to exist); "copy" clones rows and keeps the source.
type RenameMode string

const (
	RenameModeRename RenameMode = "rename"
	RenameModeCopy   RenameMode = "copy"
)

// ftsShadowTails are FTS5's internal shadow-table suffixes. Renaming the
// parent virtual table cascades to its shadows, and direct ALTER TABLE on a
// shadow is rejected by SQLite — they're filtered out of the plan.
var ftsShadowTails = []string{"_fts_data", "_fts_idx", "_fts_docsize", "_fts_content", "_fts_config"}

func isFTSShadowTable(table string) bool {
	for _, tail := range ftsShadowTails {
		if strings.HasSuffix(table, tail) {
			return true
		}
	}
	return false
}

func isFTSVirtualTable(table string) bool {
	if isFTSShadowTable(table) {
		return false
	}
	return strings.HasSuffix(table, "_fts")
}

// TablePlan is one step in a rename plan. IsFTSVirtual is true for FTS5
// virtual tables (row_count skipped); the JSON tag keeps Python's
// "is_fts_shadow" field name for wire compatibility.
type TablePlan struct {
	SrcTable     string `json:"src_table"`
	DstTable     string `json:"dst_table"`
	RowCount     int    `json:"row_count"`
	IsFTSVirtual bool   `json:"is_fts_shadow"`
}

// RenamePlan is the full rename/copy plan generated by PlanRename.
type RenamePlan struct {
	DBPath       string      `json:"-"`
	SrcNamespace string      `json:"-"`
	DstNamespace string      `json:"-"`
	Mode         RenameMode  `json:"-"`
	Steps        []TablePlan `json:"-"`
	Conflicts    []string    `json:"-"`
}

// TotalRows sums best-effort counts (FTS virtual tables count as -1 and are
// excluded).
func (p *RenamePlan) TotalRows() int {
	total := 0
	for _, s := range p.Steps {
		if s.RowCount >= 0 {
			total += s.RowCount
		}
	}
	return total
}

// HasConflicts reports whether any destination table already exists.
func (p *RenamePlan) HasConflicts() bool {
	return len(p.Conflicts) > 0
}

// listNamespaceTables returns every user-table name beginning with
// "{namespace}_". The LIKE pattern escapes underscores so a namespace
// containing "_" doesn't over-match.
func listNamespaceTables(ctx context.Context, e DBTX, namespace string) ([]string, error) {
	escaped := strings.ReplaceAll(namespace, "_", `\_`)
	pattern := escaped + `\_%`
	rows, err := e.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type IN ('table') AND name LIKE ? ESCAPE '\\' ORDER BY name", pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func tableRowCount(ctx context.Context, e DBTX, table string) int {
	var count int
	err := e.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, table)).Scan(&count)
	if err != nil {
		return -1
	}
	return count
}

// PlanRename builds a rename plan without touching anything (read-only).
func PlanRename(ctx context.Context, dbPath, srcNamespace, dstNamespace string, mode RenameMode) (*RenamePlan, error) {
	if srcNamespace == "" {
		return nil, errors.New("src_namespace cannot be empty")
	}
	if dstNamespace == "" {
		return nil, errors.New("dst_namespace cannot be empty")
	}
	if srcNamespace == dstNamespace {
		return nil, errors.New("src and dst namespaces are identical; nothing to do")
	}
	path := expandHomePath(dbPath)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database file not found: %s", path)
	}

	plan := &RenamePlan{
		DBPath:       path,
		SrcNamespace: srcNamespace,
		DstNamespace: dstNamespace,
		Mode:         mode,
		Steps:        []TablePlan{},
		Conflicts:    []string{},
	}

	// Read-only connection so planning can't accidentally mutate.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("migration: open %s read-only: %w", path, err)
	}
	defer db.Close()

	allSrcTables, err := listNamespaceTables(ctx, db, srcNamespace)
	if err != nil {
		return nil, err
	}
	var srcTables []string
	for _, t := range allSrcTables {
		if !isFTSShadowTable(t) {
			srcTables = append(srcTables, t)
		}
	}
	if len(srcTables) == 0 {
		return plan, nil
	}

	allDstTables, err := listNamespaceTables(ctx, db, dstNamespace)
	if err != nil {
		return nil, err
	}
	existingDst := map[string]bool{}
	for _, t := range allDstTables {
		if !isFTSShadowTable(t) {
			existingDst[t] = true
		}
	}

	for _, table := range srcTables {
		suffix := table[len(srcNamespace):] // includes leading underscore
		dstTable := dstNamespace + suffix
		if existingDst[dstTable] {
			plan.Conflicts = append(plan.Conflicts, dstTable)
		}
		isFTS := isFTSVirtualTable(table)
		rowCount := -1
		if !isFTS {
			rowCount = tableRowCount(ctx, db, table)
		}
		plan.Steps = append(plan.Steps, TablePlan{
			SrcTable:     table,
			DstTable:     dstTable,
			RowCount:     rowCount,
			IsFTSVirtual: isFTS,
		})
	}
	return plan, nil
}

// collectFTSVirtualTables returns every FTS5 virtual table under namespace
// (shadows excluded — dropping the virtual table cascades).
func collectFTSVirtualTables(ctx context.Context, e DBTX, namespace string) ([]string, error) {
	escaped := strings.ReplaceAll(namespace, "_", `\_`)
	rows, err := e.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name LIKE ? ESCAPE '\\' AND sql LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name",
		escaped+`\_%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// collectTriggers returns trigger names beginning with "{namespace}_".
func collectTriggers(ctx context.Context, e DBTX, namespace string) ([]string, error) {
	escaped := strings.ReplaceAll(namespace, "_", `\_`)
	rows, err := e.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE ? ESCAPE '\\' ORDER BY name",
		escaped+`\_%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// rebuildFTSForNamespace recreates the FTS5 virtual tables + sync triggers
// for ns from the package's ftsSyncSpecs (the Go port's authoritative schema
// mirror) and repopulates each index from its base table. Every value
// expression runs through hm_cjk_seg so the rebuilt index keeps the
// per-character CJK segmentation.
func rebuildFTSForNamespace(ctx context.Context, e DBTX, ns string) error {
	registerFTSFunctions() // process-wide, idempotent
	for _, spec := range ftsSyncSpecs {
		base := ns + "_" + spec.baseSuffix
		// Skip suffixes whose base table may be absent (older namespaces).
		var one int
		err := e.QueryRowContext(ctx,
			"SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", base).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		fts := ns + "_" + spec.ftsSuffix

		var colList, newVals, oldVals, colSelect []string
		for _, c := range spec.columns {
			colList = append(colList, c.name)
			newVals = append(newVals, fmt.Sprintf("%s(new.%s)", SQLFuncCJKSeg, c.name))
			oldVals = append(oldVals, fmt.Sprintf("%s(old.%s)", SQLFuncCJKSeg, c.name))
			colSelect = append(colSelect, fmt.Sprintf("%s(%s)", SQLFuncCJKSeg,
				strings.ReplaceAll(c.expr, "{row}", base)))
		}
		cols := strings.Join(colList, ", ")
		if _, err := e.ExecContext(ctx, fmt.Sprintf(
			`CREATE VIRTUAL TABLE "%s" USING fts5(%s, content='%s', content_rowid='rowid', tokenize='unicode61')`,
			fts, cols, base)); err != nil {
			return err
		}
		if _, err := e.ExecContext(ctx, fmt.Sprintf(
			`CREATE TRIGGER "%s_%s_ai" AFTER INSERT ON "%s" BEGIN
                INSERT INTO "%s"(rowid, %s) VALUES (new.%s, %s); END`,
			ns, spec.prefix, base, fts, cols, spec.rowidExpr, strings.Join(newVals, ", "))); err != nil {
			return err
		}
		if _, err := e.ExecContext(ctx, fmt.Sprintf(
			`CREATE TRIGGER "%s_%s_ad" AFTER DELETE ON "%s" BEGIN
                INSERT INTO "%s"("%s", rowid, %s) VALUES ('delete', old.%s, %s); END`,
			ns, spec.prefix, base, fts, fts, cols, spec.rowidExpr, strings.Join(oldVals, ", "))); err != nil {
			return err
		}
		if spec.withUpdate {
			if _, err := e.ExecContext(ctx, fmt.Sprintf(
				`CREATE TRIGGER "%s_%s_au" AFTER UPDATE ON "%s" BEGIN
                    INSERT INTO "%s"("%s", rowid, %s) VALUES ('delete', old.%s, %s);
                    INSERT INTO "%s"(rowid, %s) VALUES (new.%s, %s); END`,
				ns, spec.prefix, base, fts, fts, cols, spec.rowidExpr, strings.Join(oldVals, ", "),
				fts, cols, spec.rowidExpr, strings.Join(newVals, ", "))); err != nil {
				return err
			}
		}
		if _, err := e.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO "%s"(rowid, %s) SELECT rowid, %s FROM "%s"`,
			fts, cols, strings.Join(colSelect, ", "), base)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyRename executes the plan. Returns the number of tables affected.
//
// Atomic: the whole operation runs inside one SQLite transaction. After a
// rename-mode move, FTS5 virtual tables and sync triggers are dropped and
// rebuilt under the destination namespace (ALTER TABLE can't fix the FTS
// content= parameter or the 'delete' sentinel column name), then the index
// is repopulated from the renamed base tables.
func ApplyRename(ctx context.Context, plan *RenamePlan, allowOverwrite bool) (int, error) {
	if plan == nil || len(plan.Steps) == 0 {
		return 0, nil
	}
	if plan.HasConflicts() && !allowOverwrite {
		return 0, fmt.Errorf("destination tables already exist: [%s]; pass allow_overwrite=true to drop them first",
			quoteJoin(plan.Conflicts))
	}
	if plan.Mode != RenameModeRename && plan.Mode != RenameModeCopy {
		return 0, fmt.Errorf("unknown mode: %q", plan.Mode)
	}

	db, err := sql.Open("sqlite", filepath.ToSlash(plan.DBPath))
	if err != nil {
		return 0, fmt.Errorf("migration: open %s: %w", plan.DBPath, err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("migration: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if allowOverwrite {
		for _, conflict := range plan.Conflicts {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, conflict)); err != nil {
				return 0, err
			}
		}
	}

	switch plan.Mode {
	case RenameModeRename:
		// Phase 1: drop all FTS virtual tables + triggers under the source
		// namespace BEFORE renaming the base tables.
		ftsTables, err := collectFTSVirtualTables(ctx, tx, plan.SrcNamespace)
		if err != nil {
			return 0, err
		}
		triggers, err := collectTriggers(ctx, tx, plan.SrcNamespace)
		if err != nil {
			return 0, err
		}
		for _, trig := range triggers {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS "%s"`, trig)); err != nil {
				return 0, err
			}
		}
		for _, fts := range ftsTables {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, fts)); err != nil {
				return 0, err
			}
		}

		// Phase 2: rename the remaining base tables.
		for _, step := range plan.Steps {
			if step.IsFTSVirtual {
				continue // dropped above
			}
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`ALTER TABLE "%s" RENAME TO "%s"`, step.SrcTable, step.DstTable)); err != nil {
				return 0, err
			}
		}

		// Phase 3: rebuild FTS + triggers under the destination namespace.
		if err := rebuildFTSForNamespace(ctx, tx, plan.DstNamespace); err != nil {
			return 0, err
		}

	case RenameModeCopy:
		// Copy each table's DDL verbatim (renamed), then INSERT ... SELECT
		// the rows. Python uses CREATE TABLE AS SELECT, which drops every
		// constraint — the copied namespace then breaks on its first upsert
		// ("ON CONFLICT clause does not match any PRIMARY KEY"), voiding the
		// documented "instantiate a fresh Memory afterwards" promise.
		// Preserving the source DDL keeps the copy fully functional; the
		// fresh Memory open still recreates indexes / triggers / FTS (IF NOT
		// EXISTS) and rebuilds the FTS index via the cleared marker below.
		for _, step := range plan.Steps {
			if step.IsFTSVirtual {
				continue
			}
			var srcDDL sql.NullString
			if err := tx.QueryRowContext(ctx,
				"SELECT sql FROM sqlite_master WHERE type='table' AND name=?", step.SrcTable).Scan(&srcDDL); err != nil {
				return 0, err
			}
			ddl := srcDDL.String
			if i := strings.Index(ddl, step.SrcTable); i >= 0 {
				ddl = ddl[:i] + step.DstTable + ddl[i+len(step.SrcTable):]
			} else {
				ddl = fmt.Sprintf(`CREATE TABLE "%s" AS SELECT * FROM "%s"`, step.DstTable, step.SrcTable)
			}
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`INSERT INTO "%s" SELECT * FROM "%s"`, step.DstTable, step.SrcTable)); err != nil {
				return 0, err
			}
		}
		// The copied {dst}_meta carries the source's fts_text_version, which
		// would make the destination's first init skip the FTS rebuild and
		// leave the copy unsearchable — clear the marker.
		metaTable := plan.DstNamespace + "_meta"
		var one int
		err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", metaTable).Scan(&one)
		if err == nil {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM "%s" WHERE key = 'fts_text_version'`, metaTable)); err != nil {
				return 0, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("migration: commit: %w", err)
	}
	committed = true
	return len(plan.Steps), nil
}

// quoteJoin renders a string list like Python's repr of a list of strings.
func quoteJoin(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, strconv.Quote(s))
	}
	return strings.Join(quoted, " ")
}

// ---------------------------------------------------------------------------
// Preview (preview.py)
// ---------------------------------------------------------------------------

// RenderRenamePlan renders plan as "text" (terminal table) or "json"
// (machine-parsable). Format-only — never executes anything.
func RenderRenamePlan(plan *RenamePlan, format string) (string, error) {
	switch format {
	case "json":
		return renderRenamePlanJSON(plan)
	case "text":
		return renderRenamePlanText(plan), nil
	}
	return "", fmt.Errorf("unknown preview format: %q", format)
}

func renderRenamePlanText(plan *RenamePlan) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Migration plan: %s\n", plan.Mode)
	fmt.Fprintf(&out, "  db:   %s\n", plan.DBPath)
	fmt.Fprintf(&out, "  src:  %s\n", plan.SrcNamespace)
	fmt.Fprintf(&out, "  dst:  %s\n", plan.DstNamespace)
	out.WriteString("\n")

	if len(plan.Steps) == 0 {
		fmt.Fprintf(&out, "(no tables found for namespace '%s')\n", plan.SrcNamespace)
		return out.String()
	}

	srcW, dstW := 8, 8
	for _, s := range plan.Steps {
		if len(s.SrcTable) > srcW {
			srcW = len(s.SrcTable)
		}
		if len(s.DstTable) > dstW {
			dstW = len(s.DstTable)
		}
	}

	header := fmt.Sprintf("%-*s  →  %-*s  rows  kind", srcW, "src", dstW, "dst")
	out.WriteString(header + "\n")
	// Python len() counts characters; count runes so the multi-byte arrow
	// doesn't stretch the rule line by 2.
	out.WriteString(strings.Repeat("-", utf8.RuneCountInString(header)) + "\n")
	for _, s := range plan.Steps {
		rowsLabel := fmt.Sprintf("%4d", s.RowCount)
		if s.RowCount < 0 {
			rowsLabel = "   —"
		}
		kind := "data"
		if s.IsFTSVirtual {
			kind = "fts"
		}
		fmt.Fprintf(&out, "%-*s  →  %-*s  %s  %s\n", srcW, s.SrcTable, dstW, s.DstTable, rowsLabel, kind)
	}
	out.WriteString("\n")

	if len(plan.Conflicts) > 0 {
		fmt.Fprintf(&out, "⚠ Conflicts (%d):\n", len(plan.Conflicts))
		for _, c := range plan.Conflicts {
			fmt.Fprintf(&out, "  - %s\n", c)
		}
		out.WriteString("  Use --allow-overwrite to drop these before rename.\n")
		out.WriteString("\n")
	}

	fmt.Fprintf(&out, "Total: %d tables, %d rows.\n", len(plan.Steps), plan.TotalRows())
	return out.String()
}

type renameStepJSON struct {
	SrcTable     string `json:"src_table"`
	DstTable     string `json:"dst_table"`
	RowCount     int    `json:"row_count"`
	IsFTSShadow  bool   `json:"is_fts_shadow"`
}

type renamePlanJSON struct {
	Mode         string           `json:"mode"`
	DBPath       string           `json:"db_path"`
	SrcNamespace string           `json:"src_namespace"`
	DstNamespace string           `json:"dst_namespace"`
	TotalRows    int              `json:"total_rows"`
	HasConflicts bool             `json:"has_conflicts"`
	Conflicts    []string         `json:"conflicts"`
	Steps        []renameStepJSON `json:"steps"`
}

func renderRenamePlanJSON(plan *RenamePlan) (string, error) {
	steps := make([]renameStepJSON, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		steps = append(steps, renameStepJSON{
			SrcTable:    s.SrcTable,
			DstTable:    s.DstTable,
			RowCount:    s.RowCount,
			IsFTSShadow: s.IsFTSVirtual,
		})
	}
	conflicts := plan.Conflicts
	if conflicts == nil {
		conflicts = []string{}
	}
	payload := renamePlanJSON{
		Mode:         string(plan.Mode),
		DBPath:       plan.DBPath,
		SrcNamespace: plan.SrcNamespace,
		DstNamespace: plan.DstNamespace,
		TotalRows:    plan.TotalRows(),
		HasConflicts: plan.HasConflicts(),
		Conflicts:    conflicts,
		Steps:        steps,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ---------------------------------------------------------------------------
// Backfill (backfill.py)
// ---------------------------------------------------------------------------

// DefaultRateLimitPerMinute caps LLM calls per 60-second sliding window
// (D48-B). One LLM call per processed session.
const DefaultRateLimitPerMinute = 30

// BackfillSessionResult is the outcome of backfilling one session.
type BackfillSessionResult struct {
	SessionID         string  `json:"session_id"`
	RawEventCount     int     `json:"raw_event_count"`
	CandidateCount    int     `json:"candidate_count"`
	PromotedAtomCount int     `json:"promoted_atom_count"`
	SkippedReason     *string `json:"skipped_reason"`
}

// BackfillSummary aggregates one namespace backfill.
type BackfillSummary struct {
	SessionsSeen      int                     `json:"sessions_seen"`
	SessionsProcessed int                     `json:"sessions_processed"`
	SessionsSkipped   int                     `json:"sessions_skipped"`
	TotalCandidates   int                     `json:"total_candidates"`
	TotalPromoted     int                     `json:"total_promoted"`
	LLMCalls          int                     `json:"llm_calls"`
	StartedAt         time.Time               `json:"started_at"`
	FinishedAt        *time.Time              `json:"finished_at"`
	Sessions          []BackfillSessionResult `json:"sessions"`
}

// BackfillOptions customizes BackfillNamespace. RateLimitPerMinute zero
// disables limiting entirely (unit-test friendly).
type BackfillOptions struct {
	Extractor                          *CandidateExtractor
	Since                              *time.Time
	RateLimitPerMinute                 int
	SkipSessionsWithExistingCandidates bool
	ResumeFrom                         string
	Promote                            bool
	PromotionHook                      LLMEscalationHook
	limiterWindow                      time.Duration // test hook; default 60s
}

// slidingWindowLimiter allows at most cap events per window. cap <= 0
// disables limiting entirely.
type slidingWindowLimiter struct {
	cap    int
	window time.Duration
	events []time.Time
}

func newSlidingWindowLimiter(cap int, window time.Duration) *slidingWindowLimiter {
	if window <= 0 {
		window = 60 * time.Second
	}
	return &slidingWindowLimiter{cap: cap, window: window}
}

// acquire blocks until a new call is allowed; returns the slept duration.
func (l *slidingWindowLimiter) acquire() time.Duration {
	if l.cap <= 0 {
		return 0
	}
	now := time.Now()
	cutoff := now.Add(-l.window)
	for len(l.events) > 0 && l.events[0].Before(cutoff) {
		l.events = l.events[1:]
	}
	var slept time.Duration
	if len(l.events) >= l.cap {
		waitUntil := l.events[0].Add(l.window)
		slept = waitUntil.Sub(now)
		if slept < 0 {
			slept = 0
		}
		if slept > 0 {
			time.Sleep(slept)
		}
		now = time.Now()
		cutoff = now.Add(-l.window)
		for len(l.events) > 0 && l.events[0].Before(cutoff) {
			l.events = l.events[1:]
		}
	}
	l.events = append(l.events, now)
	return slept
}

// collectBackfillSessions returns session ids with raw events at or after
// since, ordered by earliest event timestamp ascending so resume-from is
// meaningful.
func collectBackfillSessions(ctx context.Context, m *Memory, since *time.Time) ([]string, error) {
	events, err := m.ListRaw(ctx, RawEventFilter{Limit: unlimitedRows})
	if err != nil {
		return nil, err
	}
	type sess struct {
		id string
		ts time.Time
	}
	sessions := map[string]time.Time{}
	for _, e := range events {
		if since != nil && e.Timestamp.Before(*since) {
			continue
		}
		if e.SessionID == nil || *e.SessionID == "" {
			continue
		}
		if prev, ok := sessions[*e.SessionID]; !ok || e.Timestamp.Before(prev) {
			sessions[*e.SessionID] = e.Timestamp
		}
	}
	ids := make([]string, 0, len(sessions))
	for sid := range sessions {
		ids = append(ids, sid)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		return sessions[ids[i]].Before(sessions[ids[j]])
	})
	return ids, nil
}

// dropUntilAfter returns the suffix of sessions AFTER pivot; a missing pivot
// returns the original list (defensive — over-replay beats silent skip).
func dropUntilAfter(sessions []string, pivot string) []string {
	for i, sid := range sessions {
		if sid == pivot {
			return sessions[i+1:]
		}
	}
	return sessions
}

// BackfillNamespace replays the candidate extractor over historical raw
// events, session by session, optionally promoting so atoms / entities /
// journal stay consistent.
func BackfillNamespace(ctx context.Context, m *Memory, opts BackfillOptions) (*BackfillSummary, error) {
	summary := &BackfillSummary{
		StartedAt: time.Now().UTC(),
		Sessions:  []BackfillSessionResult{},
	}
	extractor := opts.Extractor
	if extractor == nil {
		extractor = NewCandidateExtractor(NoopLLMClient{})
	}
	window := opts.limiterWindow
	limiter := newSlidingWindowLimiter(opts.RateLimitPerMinute, window)

	sessions, err := collectBackfillSessions(ctx, m, opts.Since)
	if err != nil {
		return nil, err
	}
	if opts.ResumeFrom != "" {
		sessions = dropUntilAfter(sessions, opts.ResumeFrom)
	}
	summary.SessionsSeen = len(sessions)

	existingCandidateSessions := map[string]bool{}
	if opts.SkipSessionsWithExistingCandidates {
		cands, err := m.ListCandidates(ctx, CandidateFilter{Limit: unlimitedRows})
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
			if c.SessionID != nil {
				existingCandidateSessions[*c.SessionID] = true
			}
		}
	}

	for _, sid := range sessions {
		sidCopy := sid
		rawEvents, err := m.ListRaw(ctx, RawEventFilter{SessionID: &sidCopy, Limit: unlimitedRows})
		rawCount := 0
		if err == nil {
			rawCount = len(rawEvents)
		}

		if existingCandidateSessions[sid] {
			reason := "already has candidates"
			summary.Sessions = append(summary.Sessions, BackfillSessionResult{
				SessionID:     sid,
				RawEventCount: rawCount,
				SkippedReason: &reason,
			})
			summary.SessionsSkipped++
			continue
		}

		// Throttle BEFORE the LLM call so a burst of sessions doesn't all
		// hit the host within 1 ms.
		limiter.acquire()

		// extract_session: pull the session's raw events, run the extractor,
		// persist candidates. Driver errors (list_raw failing) → skipped;
		// LLM failures come back inside the result as failure_reason and
		// still count as a processed session with zero candidates.
		events, err := m.ListRaw(ctx, RawEventFilter{SessionID: &sidCopy, Limit: 10_000})
		if err != nil {
			reason := fmt.Sprintf("extractor_error: %v", err)
			summary.Sessions = append(summary.Sessions, BackfillSessionResult{
				SessionID:     sid,
				RawEventCount: rawCount,
				SkippedReason: &reason,
			})
			summary.SessionsSkipped++
			continue
		}
		var result *ExtractionResult
		if len(events) == 0 {
			result = &ExtractionResult{}
		} else {
			result = extractor.Extract(ctx, events, &sidCopy)
		}
		if err := persistBackfillCandidates(ctx, m, result.Candidates); err != nil {
			return nil, err
		}
		summary.LLMCalls++

		candidates := result.Candidates
		promoted := 0
		if opts.Promote && len(candidates) > 0 {
			worker := NewPromotionWorker(m, opts.PromotionHook)
			promo, err := worker.Promote(ctx, candidates)
			if err != nil {
				return nil, err
			}
			promoted = promo.Promoted
			summary.LLMCalls += promo.LLMCalls
		}

		summary.Sessions = append(summary.Sessions, BackfillSessionResult{
			SessionID:         sid,
			RawEventCount:     rawCount,
			CandidateCount:    len(candidates),
			PromotedAtomCount: promoted,
		})
		summary.SessionsProcessed++
		summary.TotalCandidates += len(candidates)
		summary.TotalPromoted += promoted
	}

	finished := time.Now().UTC()
	summary.FinishedAt = &finished
	return summary, nil
}

// persistBackfillCandidates saves extracted candidates via AddCandidates
// (duplicate-assertion skip protection), mirroring extract_session's
// memory.add_candidates persist step.
func persistBackfillCandidates(ctx context.Context, m *Memory, candidates []*Candidate) error {
	if len(candidates) == 0 {
		return nil
	}
	return m.AddCandidates(ctx, candidates)
}
