// Package memory — EntityPage helpers (pipeline/page/{headline,
// user_notes,trigger}.py).
//
// Headline shaping (D32-A): the recall path must NOT do markdown
// parsing to extract a hot summary, so the headline is precomputed at
// write time in its own column, defensively truncated to a hard cap.
//
// User notes (D34-A): a literal `## My Notes` heading is reserved for
// the human user. The LLM regenerator must never invent or modify that
// section; extract/merge/detect helpers below enforce the contract.
//
// Trigger (D33-B): marking pages dirty is zero-LLM / zero-heavy-IO —
// just flipping a boolean so the async cron worker picks the entity up.
package memory

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// headline.py
// ---------------------------------------------------------------------------

// HeadlineMaxChars is the hard cap on headline length (Unicode
// characters). Single-character logic is intentional: full-width
// Chinese characters count as 1, the same as ASCII letters.
const HeadlineMaxChars = 30

// headlineEllipsis is appended when a headline gets cut.
const headlineEllipsis = "…"

// TruncateHeadline trims text to maxChars characters, appending … if
// cut. Leading/trailing whitespace is stripped and internal newlines
// collapse to single spaces (headlines must be single-line). Returns
// the empty string for whitespace-only input. The result never exceeds
// maxChars characters *including* the ellipsis.
func TruncateHeadline(text string) string { return truncateHeadlineWith(text, HeadlineMaxChars) }

func truncateHeadlineWith(text string, maxChars int) string {
	if text == "" {
		return ""
	}
	// strings.Fields + Join mirrors Python " ".join(text.split()).
	flattened := strings.Join(strings.Fields(text), " ")
	if flattened == "" {
		return ""
	}
	if runeLen(flattened) <= maxChars {
		return flattened
	}
	if maxChars <= 1 {
		return truncateRunes(flattened, maxChars)
	}
	// Reserve one slot for the ellipsis.
	return truncateRunes(flattened, maxChars-1) + headlineEllipsis
}

// CoerceHeadline picks the best non-empty headline candidate and
// truncates it. Used by the regenerator after parsing LLM output: an
// empty / whitespace-only headline falls back to a deterministic
// derivation (typically canonical_name + first atom assertion). The
// candidate comes straight from decoded JSON, so it is typed as any
// (Python: str | None); non-string values behave like None.
func CoerceHeadline(candidate any, fallback string) string {
	primary := ""
	if s, ok := candidate.(string); ok {
		primary = strings.TrimSpace(s)
	}
	if primary != "" {
		return TruncateHeadline(primary)
	}
	return TruncateHeadline(fallback)
}

// ---------------------------------------------------------------------------
// user_notes.py
// ---------------------------------------------------------------------------

// UserNotesHeading is the exact markdown heading text that marks the
// user-editable section. The ASCII heading text is the contract — a
// single project-wide constant, not a user-tunable string, so the LLM
// prompt and parser stay in lockstep.
const UserNotesHeading = "## My Notes"

// userNotesHeadingRe matches a line that is exactly "## My Notes" (with
// optional surrounding whitespace), case-sensitive. One or more spaces
// after ## are tolerated (Python truth: "##  My Notes" matches too);
// the heading text itself is the tight contract.
var userNotesHeadingRe = regexp.MustCompile(`(?m)^##\s+My Notes\s*$`)

// ExtractUserNotes splits a markdown body into (everything-before-notes,
// notes-block). The notes block runs from the heading line through the
// end of the document — once the user opens a notes section, everything
// they wrote until EOF is theirs (we intentionally do NOT stop at the
// next ## heading). No heading present → (markdown, "").
func ExtractUserNotes(markdown string) (string, string) {
	if markdown == "" {
		return "", ""
	}
	loc := userNotesHeadingRe.FindStringIndex(markdown)
	if loc == nil {
		return markdown, ""
	}
	head := strings.TrimRight(markdown[:loc[0]], " \t\n\r")
	notes := strings.TrimRight(markdown[loc[0]:], " \t\n\r")
	return head, notes
}

// HasUserNotes reports whether markdown contains a "## My Notes"
// heading.
func HasUserNotes(markdown string) bool {
	return userNotesHeadingRe.MatchString(markdown)
}

// MergeUserNotes splices preservedNotes into generatedBody.
//
//   - Empty preservedNotes → generatedBody, right-trimmed.
//   - If generatedBody already contains a "## My Notes" block, that
//     LLM-emitted version is DROPPED (the LLM is forbidden to write
//     into the notes section; defensive normalization).
//   - The preserved block is appended with one blank line separator.
func MergeUserNotes(generatedBody, preservedNotes string) string {
	if preservedNotes == "" {
		return strings.TrimRight(generatedBody, " \t\n\r")
	}
	cleanedBody, _ := ExtractUserNotes(generatedBody)
	cleanedBody = strings.TrimRight(cleanedBody, " \t\n\r")
	if cleanedBody == "" {
		return strings.TrimRight(preservedNotes, " \t\n\r")
	}
	return cleanedBody + "\n\n" + strings.TrimRight(preservedNotes, " \t\n\r")
}

// DetectDroppedNotes returns true if before had a notes block but after
// doesn't. The regenerator uses this as a post-merge sanity check and
// MUST refuse the regen when it fires (keep the old summary).
func DetectDroppedNotes(before, after string) bool {
	return HasUserNotes(before) && !HasUserNotes(after)
}

// ---------------------------------------------------------------------------
// trigger.py
// ---------------------------------------------------------------------------

// MarkEntityDirtyAfterPromote marks the page for entityID as dirty.
// Called from the promotion worker immediately after a new atom has
// been persisted and the entity's atom_count bumped. Idempotent. Never
// errors on missing pages — MarkEntityPageDirty inserts a stub row if
// needed.
func MarkEntityDirtyAfterPromote(ctx context.Context, mem *Memory, entityID string, when *time.Time) error {
	w := time.Now().UTC()
	if when != nil {
		w = *when
	}
	return mem.MarkEntityPageDirty(ctx, entityID, &w)
}

// ShouldRegenerate decides whether the cron worker should attempt this
// page now. Returns (true, "") when regeneration should proceed;
// otherwise (false, reason) with reason "clean" (not dirty) or
// "too_many_failures" (attempt cap burned). A page at or above
// backoffAfterFailures returns (true, "backoff") — a soft hint the
// caller may still act on.
func ShouldRegenerate(page *EntityPage, maxAttemptCount, backoffAfterFailures int) (bool, string) {
	if maxAttemptCount <= 0 {
		maxAttemptCount = 5
	}
	if backoffAfterFailures <= 0 {
		backoffAfterFailures = 3
	}
	if !page.Dirty {
		return false, "clean"
	}
	if page.RegenAttemptCount >= maxAttemptCount {
		return false, "too_many_failures"
	}
	if page.RegenAttemptCount >= backoffAfterFailures {
		// Soft hint — caller may still proceed.
		return true, "backoff"
	}
	return true, ""
}
