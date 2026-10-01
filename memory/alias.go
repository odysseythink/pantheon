package memory

import (
	"regexp"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Alias normalization helpers.
//
// Mirrors src/octop_memory/domain/alias.py. Pulled into its own file because
// both the promotion checks and review CLIs need the same normalization
// rule, and the rule is a quiet load-bearing piece of the dedup story
// (entity match correctness depends on every caller agreeing on what "same
// string" means).
//
// Normalization (single, fixed scheme — D31 deliberately keeps it simple and
// language-agnostic):
//
//  1. Unicode NFKC fold (full-width → half-width, ligatures → letters)
//  2. Case fold (Python str.casefold equivalent: full case folding)
//  3. Collapse internal whitespace runs to a single space
//  4. Strip leading/trailing whitespace
//
// We do NOT strip punctuation: "GPT-4" and "GPT 4" SHOULD remain different
// aliases. If the user wants them merged, they merge them explicitly via the
// candidate review flow.

var whitespaceRe = regexp.MustCompile(`\s+`)

// caseFolder implements Unicode full case folding, the Go equivalent of
// Python's str.casefold().
var caseFolder = cases.Fold()

// NormalizeAlias applies the canonical alias-normalization scheme.
//
// Empty-string-in / empty-string-out is allowed (caller decides whether to
// skip). The function is total: any input string maps to some output string
// with no exceptions.
func NormalizeAlias(value string) string {
	if value == "" {
		return ""
	}
	nfkc := norm.NFKC.String(value)
	lowered := caseFolder.String(nfkc)
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(lowered, " "))
}
