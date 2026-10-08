package memory

import (
	"regexp"
	"strings"
)

// CJK-aware text normalization for FTS5 indexing and querying.
//
// Mirrors src/octop_memory/storage/backends/fts_text.py.
//
// FTS5's unicode61 tokenizer treats CJK ideographs as token characters, so a
// contiguous Han run like "部署方案从docker切换" becomes ONE giant token —
// queries for "部署" or even the embedded "docker" match nothing. The fix is
// to segment text into per-character CJK tokens on BOTH sides:
//
//   - Index side: every FTS write goes through SegmentCJK (via the
//     hm_cjk_seg SQL function registered on the driver, called from the
//     FTS-sync triggers). "部署方案" is indexed as the token sequence
//     部 署 方 案.
//   - Query side: FTSMatchPhrase applies the same segmentation and wraps the
//     result in a quoted FTS5 phrase, so "部署" becomes the phrase "部 署"
//     which matches the adjacent tokens 部, 署.
//
// The transform is deterministic, which matters for external-content FTS
// tables: the 'delete' command must receive byte-identical values to what
// was originally inserted.
//
// ASCII / Latin text passes through unchanged, so English-only databases
// behave exactly as before (a phrase query stays a phrase query).

// FTS_TEXT_VERSION is bumped whenever SegmentCJK output changes for any
// input. Stored per-namespace in the {ns}_meta table; a mismatch at backend
// init triggers a full FTS trigger re-creation + index rebuild so old
// databases pick up the new segmentation transparently.
const FTS_TEXT_VERSION = "2"

// cjkCharRe covers CJK Unified Ideographs (U+4E00–U+9FFF), Extension A
// (U+3400–U+4DBF) and Compatibility Ideographs (U+F900–U+FAFF). Kana /
// Hangul deliberately excluded — consistent with the project's stated scope
// (the recall budget code uses the same narrow definition).
var cjkCharRe = regexp.MustCompile(`[㐀-䶿一-鿿豈-﫿]`)

// SQLFuncCJKSeg is the SQL scalar function name registered on the driver for
// the FTS-sync triggers.
const SQLFuncCJKSeg = "hm_cjk_seg"

// SegmentCJK inserts spaces around every CJK ideograph so unicode61 sees
// each character as its own token. Non-CJK text is returned untouched
// (modulo the inserted spaces at CJK boundaries).
func SegmentCJK(text string) string {
	if !cjkCharRe.MatchString(text) {
		return text
	}
	return cjkCharRe.ReplaceAllString(text, " $0 ")
}

// FTSMatchPhrase builds a safe FTS5 MATCH expression: segment CJK, escape
// embedded double quotes, wrap the whole thing in one quoted phrase.
//
// Phrase semantics are intentionally preserved from the pre-CJK-fix
// behaviour (the whole query is one phrase); per-token OR matching is the
// recall pipeline's job, which calls this once per token.
func FTSMatchPhrase(query string) string {
	seg := SegmentCJK(query)
	return `"` + strings.ReplaceAll(seg, `"`, `""`) + `"`
}
