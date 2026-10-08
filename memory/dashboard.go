package memory

// Local dashboard REST API, ported from adapters/dashboard/server.py.
//
// Like the Python original, this reads SQLite directly (bypassing Memory and
// the LLM stack) and exposes the memory data through a small HTTP surface.
// Every user-controlled identifier passes through an allowlist before it is
// interpolated into SQL; data values are always bound as parameters.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// DashServer serves the local dashboard. StaticDir points at the frontend
// directory (index.html); an empty StaticDir disables the frontend routes.
// DefaultDBPath / DefaultNamespace mirror app.state.default_* — CLI-injected
// defaults surfaced through /api/defaults so the frontend can autofill.
type DashServer struct {
	StaticDir        string
	DefaultDBPath    string
	DefaultNamespace string
}

// ---------------------------------------------------------------------------
// Allowlists and shapes (server.py lines 37-93)
// ---------------------------------------------------------------------------

const dashMaxNamespaceLen = 128

var dashAllowedTableSuffixes = map[string]bool{
	"_raw_events":   true,
	"_candidates":   true,
	"_atoms":        true,
	"_journal":      true,
	"_episodes":     true,
	"_memory_nodes": true,
	"_entities":     true,
	"_entity_pages": true,
	"_digests":      true,
	"_meta":         true,
}

var dashAllowedOrderCols = map[string]bool{
	"rowid":         true,
	"id":            true,
	"timestamp":     true,
	"created_at":    true,
	"occurred_at":   true,
	"updated_at":    true,
	"deprecated_at": true,
	"started_at":    true,
	"failed_at":     true,
}

var dashAllowedOrderDirs = map[string]bool{"ASC": true, "DESC": true}

var dashStatsAllowedCols = map[string]bool{
	"timestamp":   true,
	"created_at":  true,
	"occurred_at": true,
}

var (
	dashNSFormatRe   = regexp.MustCompile(`^[a-z0-9_]{1,128}$`)
	dashTableShapeRe = regexp.MustCompile(`^[a-z0-9_]{1,200}$`)
	dashDBSuffixRe   = regexp.MustCompile(`(?i)\.(?:sqlite|sqlite3|db)$`)
	dashNSBadCharRe  = regexp.MustCompile(`[^a-z0-9_]`)
)

// ---------------------------------------------------------------------------
// HTTP error plumbing (mirrors fastapi.HTTPException + {"detail": ...})
// ---------------------------------------------------------------------------

type dashHTTPError struct {
	status int
	detail string
}

func (e *dashHTTPError) Error() string { return e.detail }

func dashErrf(status int, format string, args ...any) error {
	return &dashHTTPError{status: status, detail: fmt.Sprintf(format, args...)}
}

func writeDashJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // FastAPI dumps with ensure_ascii=False
	_ = enc.Encode(v)
}

func dashWriteError(w http.ResponseWriter, err error) {
	var he *dashHTTPError
	if errors.As(err, &he) {
		writeDashJSON(w, he.status, map[string]any{"detail": he.detail})
		return
	}
	writeDashJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
}

type dashHandler func(r *http.Request) (any, error)

func dashWrap(h dashHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		if err != nil {
			dashWriteError(w, err)
			return
		}
		writeDashJSON(w, http.StatusOK, v)
	}
}

// dashCORS mirrors CORSMiddleware(allow_origins/methods/headers=["*"]).
func dashCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Security helpers (server.py lines 96-162)
// ---------------------------------------------------------------------------

// dashNormNS normalizes a namespace into a valid SQLite table prefix.
func dashNormNS(namespace string) (string, error) {
	// Python len() counts code points, not bytes.
	if utf8.RuneCountInString(namespace) > dashMaxNamespaceLen {
		return "", dashErrf(http.StatusBadRequest, "namespace 长度超出限制")
	}
	return dashNSBadCharRe.ReplaceAllString(strings.ToLower(namespace), "_"), nil
}

// dashSafeTableName builds a safe table name after validating ns and suffix.
func dashSafeTableName(ns, suffix string) (string, error) {
	if !dashNSFormatRe.MatchString(ns) {
		return "", dashErrf(http.StatusBadRequest, "namespace 格式非法")
	}
	if !dashAllowedTableSuffixes[suffix] {
		return "", dashErrf(http.StatusBadRequest, "非法的表名后缀: %s", pyReprScalar(suffix))
	}
	return ns + suffix, nil
}

func dashSafeOrderCol(col string) (string, error) {
	if !dashAllowedOrderCols[col] {
		return "", dashErrf(http.StatusBadRequest, "非法的排序列: %s", pyReprScalar(col))
	}
	return col, nil
}

func dashSafeOrderDir(direction string) (string, error) {
	upper := strings.ToUpper(direction)
	if !dashAllowedOrderDirs[upper] {
		return "", dashErrf(http.StatusBadRequest, "非法的排序方向: %s", pyReprScalar(direction))
	}
	return upper, nil
}

// dashSafeDBPath resolves and validates a database path (anti path-traversal).
func dashSafeDBPath(dbPath string) (string, error) {
	base := expandHomePath(dbPath)
	expanded, err := filepath.Abs(base)
	if err != nil {
		expanded = base
	}
	if !dashDBSuffixRe.MatchString(expanded) {
		return "", dashErrf(http.StatusBadRequest, "db_path 必须是 .sqlite / .sqlite3 / .db 文件")
	}
	return expanded, nil
}

func dashFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dashOpenDB opens the SQLite database; readonly by default.
//
// Write connections must have hm_cjk_seg available (FTS-sync triggers call
// it); Go registers driver-side and process-wide, so calling the idempotent
// registerFTSFunctions covers every pooled connection — the equivalent of
// Python's per-connection create_function fallback.
func dashOpenDB(dbPath string, readonly bool) (*sql.DB, error) {
	expanded, err := dashSafeDBPath(dbPath)
	if err != nil {
		return nil, err
	}
	if !dashFileExists(expanded) {
		return nil, dashErrf(http.StatusNotFound,
			"数据库文件不存在: %s。请先与 AI 进行对话以生成记忆数据。", expanded)
	}
	var dsn string
	if readonly {
		dsn = "file:" + filepath.ToSlash(expanded) + "?mode=ro"
	} else {
		registerFTSFunctions()
		dsn = filepath.ToSlash(expanded)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, dashErrf(http.StatusInternalServerError, "数据库打开失败: %v", err)
	}
	return db, nil
}

// dashOpenForRequest applies the shared per-request opening sequence:
// namespace normalization first, then the db path checks inside dashOpenDB
// (same precedence as the Python handlers).
func dashOpenForRequest(r *http.Request, readonly bool) (*sql.DB, string, error) {
	ns, err := dashNormNS(r.URL.Query().Get("namespace"))
	if err != nil {
		return nil, "", err
	}
	db, err := dashOpenDB(r.URL.Query().Get("db_path"), readonly)
	if err != nil {
		return nil, "", err
	}
	return db, ns, nil
}

// ---------------------------------------------------------------------------
// Row / pagination helpers (server.py lines 165-240)
// ---------------------------------------------------------------------------

// dashJSONRowFields are the columns stored as JSON strings in SQLite.
var dashJSONRowFields = []string{
	"payload", "raw_event_ids", "search_terms", "people", "topics",
	"aliases", "digest_ids", "before", "after",
}

// dashRowsToList converts rows into JSON-serializable maps, decoding the
// JSON string fields when present (failures keep the raw string, as in
// Python's suppressed json.JSONDecodeError).
func dashRowsToList(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[c] = v
		}
		for _, key := range dashJSONRowFields {
			if s, ok := m[key].(string); ok {
				var decoded any
				if json.Unmarshal([]byte(s), &decoded) == nil {
					m[key] = decoded
				}
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func dashTableExists(db *sql.DB, table string) bool {
	var name string
	return db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&name) == nil
}

// dashVerifiedTable re-fetches the identifier from sqlite_master so the
// string interpolated into SQL comes from SQLite, not user input.
func dashVerifiedTable(db *sql.DB, table string) (string, error) {
	var name string
	if err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

// dashPaginateQuery runs a paginated query after identifier allowlist
// validation. Missing tables yield an empty page instead of an error.
func dashPaginateQuery(db *sql.DB, table, orderCol, order string, page, pageSize int, where string, params []any) (map[string]any, error) {
	// Defense in depth: revalidate the table identifier shape.
	if !dashTableShapeRe.MatchString(table) {
		return nil, dashErrf(http.StatusBadRequest, "非法的表名")
	}
	safeCol, err := dashSafeOrderCol(orderCol)
	if err != nil {
		return nil, err
	}
	safeOrder, err := dashSafeOrderDir(order)
	if err != nil {
		return nil, err
	}
	if !dashTableExists(db, table) {
		return map[string]any{
			"items": []map[string]any{}, "total": 0,
			"page": page, "page_size": pageSize, "has_more": false,
		}, nil
	}
	verified, err := dashVerifiedTable(db, table)
	if err != nil {
		return nil, err
	}

	whereClause := ""
	if where != "" {
		whereClause = "WHERE " + where
	}
	var total int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM "+verified+" "+whereClause, params...,
	).Scan(&total); err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	dataParams := append(append([]any{}, params...), pageSize, offset)
	rows, err := db.Query(
		"SELECT * FROM "+verified+" "+whereClause+
			" ORDER BY "+safeCol+" "+safeOrder+" LIMIT ? OFFSET ?",
		dataParams...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := dashRowsToList(rows)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"items": items, "total": total,
		"page": page, "page_size": pageSize,
		"has_more": offset+pageSize < total,
	}, nil
}

// ---------------------------------------------------------------------------
// Query parameter helpers (FastAPI Query(...) equivalents)
// ---------------------------------------------------------------------------

// dashPageParams parses page (>=1, default 1) and page_size (1..100,
// default 20); FastAPI responds 422 for out-of-range or non-integer input.
func dashPageParams(r *http.Request) (int, int, error) {
	page, pageSize := 1, 20
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, dashErrf(http.StatusUnprocessableEntity, "page 必须是 >= 1 的整数")
		}
		page = n
	}
	if v := r.URL.Query().Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, dashErrf(http.StatusUnprocessableEntity, "page_size 必须在 1 到 100 之间")
		}
		pageSize = n
	}
	return page, pageSize, nil
}

// dashParseBool accepts the pydantic bool spellings for query parameters.
func dashParseBool(name, v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on", "y", "t":
		return true, nil
	case "false", "0", "no", "off", "n", "f":
		return false, nil
	}
	return false, dashErrf(http.StatusUnprocessableEntity, "%s 必须是布尔值", name)
}

// ---------------------------------------------------------------------------
// Routes — read API (server.py lines 248-570)
// ---------------------------------------------------------------------------

// Handler returns the fully wired HTTP handler (with permissive CORS, like
// the FastAPI middleware stack).
func (s *DashServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	if s.StaticDir != "" {
		mux.Handle("GET /static/",
			http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	}
	mux.HandleFunc("GET /api/connect", dashWrap(s.handleConnect))
	mux.HandleFunc("GET /api/stats", dashWrap(s.handleStats))
	mux.HandleFunc("GET /api/raw_events", dashWrap(s.handleRawEvents))
	mux.HandleFunc("GET /api/candidates", dashWrap(s.handleCandidates))
	mux.HandleFunc("GET /api/atoms", dashWrap(s.handleAtoms))
	mux.HandleFunc("GET /api/atoms/{atom_id}", dashWrap(s.handleGetAtom))
	mux.HandleFunc("GET /api/journal", dashWrap(s.handleJournal))
	mux.HandleFunc("GET /api/episodes", dashWrap(s.handleEpisodes))
	mux.HandleFunc("PATCH /api/candidates/{candidate_id}", dashWrap(s.handlePatchCandidate))
	mux.HandleFunc("DELETE /api/atoms/{atom_id}", dashWrap(s.handleDeleteAtom))
	mux.HandleFunc("DELETE /api/raw_events/{event_id}", dashWrap(s.handleDeleteRawEvent))
	mux.HandleFunc("GET /api/defaults", dashWrap(s.handleDefaults))
	mux.HandleFunc("GET /api/namespaces", dashWrap(s.handleNamespaces))
	return dashCORS(mux)
}

// RunDashServe blocks serving the dashboard on addr.
func RunDashServe(addr string, s *DashServer) error {
	return http.ListenAndServe(addr, s.Handler())
}

func (s *DashServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(filepath.Join(s.StaticDir, "index.html"))
	if err != nil {
		dashWriteError(w, dashErrf(http.StatusNotFound, "前端文件未找到"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *DashServer) handleConnect(r *http.Request) (any, error) {
	q := r.URL.Query()
	// Always validate path format through dashSafeDBPath to prevent traversal.
	expanded, err := dashSafeDBPath(q.Get("db_path"))
	if err != nil {
		return nil, err
	}
	if !dashFileExists(expanded) {
		return nil, dashErrf(http.StatusNotFound,
			"数据库文件不存在: %s。请先与 AI 进行对话以生成记忆数据。", expanded)
	}
	ns, err := dashNormNS(q.Get("namespace"))
	if err != nil {
		return nil, err
	}
	db, err := dashOpenDB(q.Get("db_path"), true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tables := map[string]any{}
	for key, suffix := range map[string]string{
		"raw_events": "_raw_events",
		"candidates": "_candidates",
		"atoms":      "_atoms",
		"journal":    "_journal",
		"episodes":   "_episodes",
	} {
		tbl, err := dashSafeTableName(ns, suffix)
		if err != nil {
			return nil, err
		}
		tables[key] = dashTableExists(db, tbl)
	}
	return map[string]any{
		"ok":        true,
		"db_path":   expanded,
		"namespace": q.Get("namespace"),
		"ns_prefix": ns,
		"tables":    tables,
	}, nil
}

func (s *DashServer) handleStats(r *http.Request) (any, error) {
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tblRaw, err := dashSafeTableName(ns, "_raw_events")
	if err != nil {
		return nil, err
	}
	tblAtoms, err := dashSafeTableName(ns, "_atoms")
	if err != nil {
		return nil, err
	}
	tblCands, err := dashSafeTableName(ns, "_candidates")
	if err != nil {
		return nil, err
	}
	tblEpisodes, err := dashSafeTableName(ns, "_episodes")
	if err != nil {
		return nil, err
	}
	tblJournal, err := dashSafeTableName(ns, "_journal")
	if err != nil {
		return nil, err
	}

	count := func(table, where string, params ...any) (int, error) {
		if !dashTableExists(db, table) {
			return 0, nil
		}
		verified, err := dashVerifiedTable(db, table)
		if err != nil {
			return 0, err
		}
		whereClause := ""
		if where != "" {
			whereClause = "WHERE " + where
		}
		var n int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM "+verified+" "+whereClause, params...,
		).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	maxTS := func(table, col string) (*string, error) {
		if !dashTableExists(db, table) {
			return nil, nil
		}
		// Validate the column name allowlist to prevent injection.
		if !dashStatsAllowedCols[col] {
			return nil, dashErrf(http.StatusBadRequest, "非法的统计列: %s", pyReprScalar(col))
		}
		verified, err := dashVerifiedTable(db, table)
		if err != nil {
			return nil, err
		}
		var ns sql.NullString
		if err := db.QueryRow(
			"SELECT MAX("+col+") FROM "+verified,
		).Scan(&ns); err != nil {
			return nil, err
		}
		if !ns.Valid {
			return nil, nil
		}
		return &ns.String, nil
	}

	rawEventsTotal, err := count(tblRaw, "")
	if err != nil {
		return nil, err
	}
	atomsActive, err := count(tblAtoms, "deprecated_at IS NULL")
	if err != nil {
		return nil, err
	}
	atomsDeprecated, err := count(tblAtoms, "deprecated_at IS NOT NULL")
	if err != nil {
		return nil, err
	}
	candidatesPending, err := count(tblCands, "status = ?", "pending")
	if err != nil {
		return nil, err
	}
	episodesTotal, err := count(tblEpisodes, "")
	if err != nil {
		return nil, err
	}
	journalTotal, err := count(tblJournal, "")
	if err != nil {
		return nil, err
	}
	lastCapture, err := maxTS(tblRaw, "timestamp")
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"raw_events":         rawEventsTotal,
		"atoms_active":       atomsActive,
		"atoms_deprecated":   atomsDeprecated,
		"atoms_total":        atomsActive + atomsDeprecated,
		"candidates_pending": candidatesPending,
		"episodes":           episodesTotal,
		"journal":            journalTotal,
		"last_capture":       lastCapture,
	}, nil
}

func (s *DashServer) handleRawEvents(r *http.Request) (any, error) {
	page, pageSize, err := dashPageParams(r)
	if err != nil {
		return nil, err
	}
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tbl, err := dashSafeTableName(ns, "_raw_events")
	if err != nil {
		return nil, err
	}
	var whereParts []string
	var params []any
	q := r.URL.Query()
	if v := q.Get("session_id"); v != "" {
		whereParts = append(whereParts, "session_id = ?")
		params = append(params, v)
	}
	if v := q.Get("event_type"); v != "" {
		whereParts = append(whereParts, "event_type = ?")
		params = append(params, v)
	}
	return dashPaginateQuery(db, tbl, "timestamp", "DESC", page, pageSize,
		strings.Join(whereParts, " AND "), params)
}

func (s *DashServer) handleCandidates(r *http.Request) (any, error) {
	page, pageSize, err := dashPageParams(r)
	if err != nil {
		return nil, err
	}
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tbl, err := dashSafeTableName(ns, "_candidates")
	if err != nil {
		return nil, err
	}
	var whereParts []string
	var params []any
	q := r.URL.Query()
	if v := q.Get("status"); v != "" {
		whereParts = append(whereParts, "status = ?")
		params = append(params, v)
	}
	if v := q.Get("candidate_type"); v != "" {
		whereParts = append(whereParts, "candidate_type = ?")
		params = append(params, v)
	}
	return dashPaginateQuery(db, tbl, "created_at", "DESC", page, pageSize,
		strings.Join(whereParts, " AND "), params)
}

func (s *DashServer) handleAtoms(r *http.Request) (any, error) {
	page, pageSize, err := dashPageParams(r)
	if err != nil {
		return nil, err
	}
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tbl, err := dashSafeTableName(ns, "_atoms")
	if err != nil {
		return nil, err
	}
	var whereParts []string
	var params []any
	q := r.URL.Query()
	includeDeprecated := false
	if v := q.Get("include_deprecated"); v != "" {
		if includeDeprecated, err = dashParseBool("include_deprecated", v); err != nil {
			return nil, err
		}
	}
	if !includeDeprecated {
		whereParts = append(whereParts, "deprecated_at IS NULL")
	}
	if v := q.Get("importance"); v != "" {
		whereParts = append(whereParts, "importance = ?")
		params = append(params, v)
	}
	return dashPaginateQuery(db, tbl, "created_at", "DESC", page, pageSize,
		strings.Join(whereParts, " AND "), params)
}

func (s *DashServer) handleGetAtom(r *http.Request) (any, error) {
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	atomTable, err := dashSafeTableName(ns, "_atoms")
	if err != nil {
		return nil, err
	}
	if !dashTableExists(db, atomTable) {
		return nil, dashErrf(http.StatusNotFound, "atoms 表不存在")
	}
	verifiedAtomTable, err := dashVerifiedTable(db, atomTable)
	if err != nil {
		return nil, err
	}
	atomID := r.PathValue("atom_id")
	rows, err := db.Query("SELECT * FROM "+verifiedAtomTable+" WHERE id = ?", atomID)
	if err != nil {
		return nil, err
	}
	items, err := dashRowsToList(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, dashErrf(http.StatusNotFound, "atom %s 不存在", pyReprScalar(atomID))
	}
	atom := items[0]

	// Attach the linked candidate when the table exists.
	candTable, err := dashSafeTableName(ns, "_candidates")
	if err != nil {
		return nil, err
	}
	if dashTableExists(db, candTable) {
		verifiedCandTable, err := dashVerifiedTable(db, candTable)
		if err != nil {
			return nil, err
		}
		candRows, err := db.Query(
			"SELECT * FROM "+verifiedCandTable+" WHERE id = ?", atom["candidate_id"])
		if err != nil {
			return nil, err
		}
		cands, err := dashRowsToList(candRows)
		candRows.Close()
		if err != nil {
			return nil, err
		}
		if len(cands) > 0 {
			atom["candidate"] = cands[0]
		} else {
			atom["candidate"] = nil
		}
	} else {
		atom["candidate"] = nil
	}
	return atom, nil
}

func (s *DashServer) handleJournal(r *http.Request) (any, error) {
	page, pageSize, err := dashPageParams(r)
	if err != nil {
		return nil, err
	}
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tbl, err := dashSafeTableName(ns, "_journal")
	if err != nil {
		return nil, err
	}
	var whereParts []string
	var params []any
	q := r.URL.Query()
	if v := q.Get("action"); v != "" {
		whereParts = append(whereParts, "action = ?")
		params = append(params, v)
	}
	if v := q.Get("actor"); v != "" {
		whereParts = append(whereParts, "actor = ?")
		params = append(params, v)
	}
	return dashPaginateQuery(db, tbl, "timestamp", "DESC", page, pageSize,
		strings.Join(whereParts, " AND "), params)
}

func (s *DashServer) handleEpisodes(r *http.Request) (any, error) {
	page, pageSize, err := dashPageParams(r)
	if err != nil {
		return nil, err
	}
	db, ns, err := dashOpenForRequest(r, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tbl, err := dashSafeTableName(ns, "_episodes")
	if err != nil {
		return nil, err
	}
	var whereParts []string
	var params []any
	if v := r.URL.Query().Get("emotion"); v != "" {
		whereParts = append(whereParts, "emotion = ?")
		params = append(params, v)
	}
	return dashPaginateQuery(db, tbl, "occurred_at", "DESC", page, pageSize,
		strings.Join(whereParts, " AND "), params)
}

// ---------------------------------------------------------------------------
// Routes — write API (server.py lines 573-665)
// ---------------------------------------------------------------------------

// dashAllowedCandidateStatuses: Python validates against a set literal whose
// repr order is hash-random, so any fixed order is equally faithful; Go
// renders the sorted order for determinism.
var dashAllowedCandidateStatuses = []string{"needs_review", "pending", "promoted", "rejected"}

func (s *DashServer) handlePatchCandidate(r *http.Request) (any, error) {
	// Body(..., embed=True): read {"status": "..."} from the JSON body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, dashErrf(http.StatusUnprocessableEntity, "请求体读取失败")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return nil, dashErrf(http.StatusUnprocessableEntity, "请求体必须是 JSON 对象")
	}
	raw, present := payload["status"]
	if !present {
		return nil, dashErrf(http.StatusUnprocessableEntity, "请求体缺少 status 字段")
	}
	status, _ := raw.(string)
	allowed := false
	for _, a := range dashAllowedCandidateStatuses {
		if status == a {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, dashErrf(http.StatusBadRequest, "status 必须是 {%s} 之一",
			strings.Join(dashAllowedCandidateStatuses, ", "))
	}

	db, ns, err := dashOpenForRequest(r, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	table, err := dashSafeTableName(ns, "_candidates")
	if err != nil {
		return nil, err
	}
	if !dashTableExists(db, table) {
		return nil, dashErrf(http.StatusNotFound, "candidates 表不存在")
	}
	verified, err := dashVerifiedTable(db, table)
	if err != nil {
		return nil, err
	}
	candidateID := r.PathValue("candidate_id")
	var id string
	if err := db.QueryRow(
		"SELECT id FROM "+verified+" WHERE id = ?", candidateID,
	).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, dashErrf(http.StatusNotFound, "candidate %s 不存在", pyReprScalar(candidateID))
		}
		return nil, err
	}
	if _, err := db.Exec(
		"UPDATE "+verified+" SET status = ? WHERE id = ?", status, candidateID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": candidateID, "status": status}, nil
}

func (s *DashServer) handleDeleteAtom(r *http.Request) (any, error) {
	db, ns, err := dashOpenForRequest(r, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	table, err := dashSafeTableName(ns, "_atoms")
	if err != nil {
		return nil, err
	}
	if !dashTableExists(db, table) {
		return nil, dashErrf(http.StatusNotFound, "atoms 表不存在")
	}
	verified, err := dashVerifiedTable(db, table)
	if err != nil {
		return nil, err
	}
	atomID := r.PathValue("atom_id")
	var id string
	if err := db.QueryRow(
		"SELECT id FROM "+verified+" WHERE id = ?", atomID,
	).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, dashErrf(http.StatusNotFound, "atom %s 不存在", pyReprScalar(atomID))
		}
		return nil, err
	}
	// datetime.now(UTC).isoformat() — pyISOFormat produces the same shape.
	now := pyISOFormat(time.Now())
	if _, err := db.Exec(
		"UPDATE "+verified+" SET deprecated_at = ? WHERE id = ?", now, atomID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": atomID, "deprecated_at": now}, nil
}

func (s *DashServer) handleDeleteRawEvent(r *http.Request) (any, error) {
	db, ns, err := dashOpenForRequest(r, false)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	table, err := dashSafeTableName(ns, "_raw_events")
	if err != nil {
		return nil, err
	}
	if !dashTableExists(db, table) {
		return nil, dashErrf(http.StatusNotFound, "raw_events 表不存在")
	}
	verified, err := dashVerifiedTable(db, table)
	if err != nil {
		return nil, err
	}
	eventID := r.PathValue("event_id")
	var id string
	if err := db.QueryRow(
		"SELECT id FROM "+verified+" WHERE id = ?", eventID,
	).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, dashErrf(http.StatusNotFound, "raw_event %s 不存在", pyReprScalar(eventID))
		}
		return nil, err
	}
	if _, err := db.Exec("DELETE FROM "+verified+" WHERE id = ?", eventID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "id": eventID, "deleted": true}, nil
}

// ---------------------------------------------------------------------------
// Routes — meta (server.py lines 668-702)
// ---------------------------------------------------------------------------

func (s *DashServer) handleDefaults(r *http.Request) (any, error) {
	return map[string]any{"db_path": s.DefaultDBPath, "namespace": s.DefaultNamespace}, nil
}

func (s *DashServer) handleNamespaces(r *http.Request) (any, error) {
	db, err := dashOpenDB(r.URL.Query().Get("db_path"), true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	namespaces := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		// Use tables ending in _raw_events as namespace anchors and validate
		// the prefix shape to filter tampered table names.
		if suffix := "_raw_events"; strings.HasSuffix(name, suffix) {
			nsPrefix := name[:len(name)-len(suffix)]
			if dashNSFormatRe.MatchString(nsPrefix) {
				namespaces = append(namespaces, nsPrefix)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(namespaces)
	return map[string]any{"namespaces": namespaces}, nil
}
