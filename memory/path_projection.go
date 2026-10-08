// Package memory — D51-C path projection (application/path_projection.py).
//
// Virtual ``path`` strings ↔ SQLite rows. The host's memory_get(path,
// from, lines) is shaped for a file-backed store; SQLite rows are
// projected onto a virtual file tree and parsed on the way back:
//
//	atom/<atom_id>.md            → AtomCard
//	page/<entity_id>.md          → EntityPage (long-form summary)
//	raw/<YYYY-MM-DD>/<id>.md     → RawEvent (date from event.timestamp)
//
// Real-file paths (indexed and served by HostFilesIndex when the bridge
// receives a --host-files-root):
//
//	MEMORY.md / USER.md / DREAMS.md, memory/<YYYY-MM-DD>.md,
//	memory/<YYYY-MM-DD>-<slug>.md, topics/<name>.md
//
// Pure: no SQLite, no I/O — parsing, formatting, rendering only.
package memory

import (
	"fmt"
	"regexp"
	"strings"
)

// PathKind discriminates parsed paths. host_* are real files written by
// the host; atom/page/raw are virtual.
type PathKind string

const (
	PathKindAtom      PathKind = "atom"
	PathKindPage      PathKind = "page"
	PathKindRaw       PathKind = "raw"
	PathKindHostRoot  PathKind = "host_root"
	PathKindHostDaily PathKind = "host_daily"
	PathKindHostDream PathKind = "host_dreams"
	PathKindHostFile  PathKind = "host_file"
)

// minPathParts is the minimum number of path segments for a valid host
// markdown path (e.g. 'memory/2024-01-01.md').
const minPathParts = 2

// PathRef is a parsed virtual or real-file path. Fields are sparse: only
// the ones relevant for the kind are set.
type PathRef struct {
	Kind    PathKind
	RawPath string // the path as the agent gave it; preserved for errors
	ID      *string
	Date    *string // YYYY-MM-DD for raw refs and host_daily refs
	Filename *string
	Slug    *string
}

// PathError is raised when a path string can't be parsed into any known
// kind. The bridge surfaces the message to the agent as a normal tool
// error so it can recover (e.g. fall back to memory_search).
type PathError struct{ msg string }

func (e *PathError) Error() string { return e.msg }

func pathErrorf(format string, args ...any) *PathError {
	return &PathError{msg: fmt.Sprintf(format, args...)}
}

// id alphabet: alphanumerics + underscore + hyphen. Atoms / events use
// uuid-ish ids, entities use canonical names — both are safe.
const (
	idPattern   = `[A-Za-z0-9_\-]+`
	datePattern = `\d{4}-\d{2}-\d{2}`
)

var (
	atomRe      = regexp.MustCompile(`^atom/(` + idPattern + `)\.md$`)
	pageRe      = regexp.MustCompile(`^page/(` + idPattern + `)\.md$`)
	rawRe       = regexp.MustCompile(`^raw/(` + datePattern + `)/(` + idPattern + `)\.md$`)
	hostDailyRe = regexp.MustCompile(`^memory/(` + datePattern + `)(?:-(` + idPattern + `))?\.md$`)
)

// ParsePath parses an agent-supplied path string into a typed PathRef.
// Most-specific patterns first. Unknown shapes raise PathError.
func ParsePath(path string) (*PathRef, error) {
	if path == "" || strings.TrimSpace(path) != path {
		return nil, pathErrorf("path must be non-empty and unpadded: %s", pyReprScalar(path))
	}

	if m := rawRe.FindStringSubmatch(path); m != nil {
		return &PathRef{Kind: PathKindRaw, RawPath: path, Date: &m[1], ID: &m[2]}, nil
	}
	if m := atomRe.FindStringSubmatch(path); m != nil {
		return &PathRef{Kind: PathKindAtom, RawPath: path, ID: &m[1]}, nil
	}
	if m := pageRe.FindStringSubmatch(path); m != nil {
		return &PathRef{Kind: PathKindPage, RawPath: path, ID: &m[1]}, nil
	}
	if m := hostDailyRe.FindStringSubmatch(path); m != nil {
		ref := &PathRef{Kind: PathKindHostDaily, RawPath: path, Date: &m[1], Filename: &path}
		if m[2] != "" {
			slug := m[2]
			ref.Slug = &slug
		}
		return ref, nil
	}
	if path == "MEMORY.md" || path == "USER.md" {
		return &PathRef{Kind: PathKindHostRoot, RawPath: path, Filename: &path}, nil
	}
	if path == "DREAMS.md" {
		return &PathRef{Kind: PathKindHostDream, RawPath: path, Filename: &path}, nil
	}
	if isSafeHostMarkdownPath(path) {
		return &PathRef{Kind: PathKindHostFile, RawPath: path, Filename: &path}, nil
	}

	return nil, pathErrorf(
		"unknown path shape: %s; expected one of atom/<id>.md, page/<entity_id>.md, raw/<YYYY-MM-DD>/<id>.md, "+
			"memory/<YYYY-MM-DD>.md, MEMORY.md, USER.md, DREAMS.md, or an indexed relative .md host file",
		pyReprScalar(path))
}

// isSafeHostMarkdownPath returns true for relative markdown paths that
// cannot escape the host index.
func isSafeHostMarkdownPath(path string) bool {
	if !strings.HasSuffix(path, ".md") || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return len(parts) >= minPathParts
}

// ---------------------------------------------------------------------------
// Formatting (row → path)
// ---------------------------------------------------------------------------

// AtomToPath formats an AtomCard as its virtual path.
func AtomToPath(atom *AtomCard) string { return "atom/" + atom.ID + ".md" }

// PageToPath formats an EntityPage as its virtual path.
func PageToPath(page *EntityPage) string { return "page/" + page.EntityID + ".md" }

// RawToPath formats a RawEvent as a virtual path. Date comes from the
// event timestamp in UTC (host_daily files carry the host's timezone,
// but that is the host's job).
func RawToPath(event *RawEvent) string {
	date := AsUtc(event.Timestamp).Format("2006-01-02")
	return "raw/" + date + "/" + event.ID + ".md"
}

// ---------------------------------------------------------------------------
// Rendering (row → markdown body)
// ---------------------------------------------------------------------------

// RenderAtomMD renders an AtomCard as a self-contained markdown doc with
// YAML front-matter + body: assertion heading and (when distinct) the
// verbatim quote as a blockquote.
func RenderAtomMD(atom *AtomCard) string {
	lines := []string{
		"---",
		"id: " + atom.ID,
		"entity_id: " + atom.EntityID,
		"importance: " + string(atom.Importance),
		"confidence: " + string(atom.Confidence),
		"occurred_at: " + pythonISOFormat(AsUtc(atom.OccurredAt)),
		"quote_event_id: " + atom.QuoteEventID,
		"---",
		"",
		"# " + atom.Assertion,
		"",
	}
	if atom.VerbatimQuote != "" && strings.TrimSpace(atom.VerbatimQuote) != strings.TrimSpace(atom.Assertion) {
		lines = append(lines, "> "+atom.VerbatimQuote, "")
	}
	if len(atom.SearchTerms) > 0 {
		lines = append(lines, "_search terms: "+strings.Join(atom.SearchTerms, ", ")+"_", "")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// RenderPageMD renders an EntityPage with front-matter. The summary may
// be empty (brand-new dirty entities); the front-matter still distinguishes
// "no summary yet" from "no such page".
func RenderPageMD(page *EntityPage) string {
	lines := []string{
		"---",
		"entity_id: " + page.EntityID,
		fmt.Sprintf("version: %d", page.SummaryVersion),
		"dirty: " + pythonBoolLiteral(page.Dirty),
		"updated_at: " + pythonISOFormat(AsUtc(page.UpdatedAt)),
		"---",
		"",
	}
	headline := strings.TrimSpace(page.Headline)
	if headline != "" {
		lines = append(lines, "# "+headline, "")
	}
	body := strings.TrimSpace(page.SummaryMarkdown)
	if body != "" {
		lines = append(lines, body)
	} else {
		lines = append(lines, "_(empty page — pending regeneration)_")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// RenderRawMD renders a RawEvent markdown view. Body is verbatim
// content; front-matter exposes role / event type / session ids.
func RenderRawMD(event *RawEvent) string {
	role := ""
	if event.Payload != nil {
		if r, ok := event.Payload["role"].(string); ok {
			role = r
		}
	}
	if role == "" {
		role = string(event.EventType)
	}
	lines := []string{
		"---",
		"id: " + event.ID,
		"event_type: " + string(event.EventType),
		"role: " + role,
		"timestamp: " + pythonISOFormat(AsUtc(event.Timestamp)),
	}
	if event.SessionID != nil {
		lines = append(lines, "session_id: "+*event.SessionID)
	}
	if event.ThreadID != nil {
		lines = append(lines, "thread_id: "+*event.ThreadID)
	}
	lines = append(lines, "---", "", event.Content)
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func pythonBoolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---------------------------------------------------------------------------
// Line slicing
// ---------------------------------------------------------------------------

// SliceLines returns a 1-indexed line range of text. fromLine defaults
// to 1; lines defaults to no limit (pass <= 0). Out-of-range slicing is
// graceful: past-the-end from_line yields "".
func SliceLines(text string, fromLine, lines int) (string, error) {
	if fromLine < 1 && fromLine != 0 {
		return "", fmt.Errorf("from_line must be >= 1, got %s", pyReprScalar(float64(fromLine)))
	}
	if lines < 1 && lines != 0 {
		return "", fmt.Errorf("lines must be >= 1, got %s", pyReprScalar(float64(lines)))
	}
	bodyLines := splitLines(text)
	start := fromLine - 1
	if start < 0 {
		start = 0
	}
	if start >= len(bodyLines) {
		return "", nil
	}
	end := len(bodyLines)
	if lines > 0 && start+lines < end {
		end = start + lines
	}
	return strings.Join(bodyLines[start:end], "\n"), nil
}

// splitLines mirrors Python str.splitlines: splits on \n / \r\n / \r
// without keeping the line terminators. A trailing terminator does NOT
// produce a trailing empty line ("a\n" → ["a"], "\n" → [""]).
func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// ExcerptMetadata builds the bookkeeping dict the agent expects alongside
// an excerpt: when the requested slice doesn't cover the whole document,
// truncated=true plus a continuation pointer so the agent can call again
// with a higher from.
func ExcerptMetadata(fullText string, fromLine, lines int) map[string]any {
	total := len(splitLines(fullText))
	start := fromLine
	if start < 1 {
		start = 1
	}
	end := total
	if lines > 0 && start+lines-1 < end {
		end = start + lines - 1
	}
	truncated := end < total || start > 1
	out := map[string]any{
		"total_lines": total,
		"from_line":   start,
		"to_line":     maxInt(start-1, end),
		"truncated":   truncated,
	}
	if end < total {
		out["continuation"] = map[string]any{"from": end + 1}
	}
	return out
}
