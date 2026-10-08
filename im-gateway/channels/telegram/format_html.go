// Package telegram ports octop_gateway.channels.telegram: the Telegram
// channel implementation (Bot API long polling) and its Markdown → HTML
// converter (octop_gateway.channels.telegram.format_html).
package telegram

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// escapeHTML escapes &, < and > (mirrors format_html._escape_html).
func escapeHTML(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

var (
	codeBlockRe = regexp.MustCompile("(?s)```([\\w\\pL\\pN]*)\\n?([\\s\\S]*?)```")
	inlineCodeRe = regexp.MustCompile("`([^`\\n]+)`")
	linkRe       = regexp.MustCompile("\\[([^\\]]+)\\]\\(([^)]+)\\)")

	hrRe      = regexp.MustCompile("(?m)^[\\*\\-_]{3,}\\s*$")
	headingRe = regexp.MustCompile("(?m)^#{1,6}\\s+(.+?)$")
	bulletRe  = regexp.MustCompile("(?m)^(\\s*)[\\*\\-]\\s+")

	spoilerRe      = regexp.MustCompile("\\|\\|(.+?)\\|\\|")
	boldItalicRe   = regexp.MustCompile("\\*{3}(.+?)\\*{3}")
	boldRe         = regexp.MustCompile("\\*{2}(.+?)\\*{2}")
	boldUnderlineRe = regexp.MustCompile("__(.+?)__")
	strikeRe       = regexp.MustCompile("~~(.+?)~~")
)

// MarkdownToTelegramHTML converts Markdown text to the Telegram Bot API HTML
// subset (mirrors format_html.markdown_to_telegram_html).
//
// Code blocks, inline code, and links are protected behind \x00PH{n}\x00
// placeholders before the remaining text is escaped, then substituted back at
// the end — exactly like the Python original.
func MarkdownToTelegramHTML(text string) string {
	if text == "" {
		return text
	}

	placeholders := make([]string, 0, 4)
	ph := func(fragment string) string {
		placeholders = append(placeholders, fragment)
		return "\x00PH" + strconv.Itoa(len(placeholders)-1) + "\x00"
	}

	// ``` fenced code blocks (DOTALL; language is \w* on the fence line).
	text = codeBlockRe.ReplaceAllStringFunc(text, func(match string) string {
		g := codeBlockRe.FindStringSubmatch(match)
		lang := strings.TrimSpace(g[1])
		code := escapeHTML(g[2])
		if lang != "" {
			return ph(fmt.Sprintf(`<pre><code class="language-%s">%s</code></pre>`, escapeHTML(lang), code))
		}
		return ph("<pre>" + code + "</pre>")
	})
	// ` inline code.
	text = inlineCodeRe.ReplaceAllStringFunc(text, func(match string) string {
		g := inlineCodeRe.FindStringSubmatch(match)
		return ph("<code>" + escapeHTML(g[1]) + "</code>")
	})
	// [text](url) links. Only < and > are percent-encoded in the href; the
	// link text is escaped.
	text = linkRe.ReplaceAllStringFunc(text, func(match string) string {
		g := linkRe.FindStringSubmatch(match)
		href := strings.ReplaceAll(g[2], "<", "%3C")
		href = strings.ReplaceAll(href, ">", "%3E")
		return ph(fmt.Sprintf(`<a href="%s">%s</a>`, href, escapeHTML(g[1])))
	})

	text = escapeHTML(text)
	text = hrRe.ReplaceAllString(text, "———")
	text = headingRe.ReplaceAllString(text, "<b>${1}</b>")

	// Blockquote grouping: consecutive "> " lines collapse into one
	// <blockquote> element.
	lines := strings.Split(text, "\n")
	resultLines := make([]string, 0, len(lines))
	var quoteBuf []string
	flushQuote := func() {
		if len(quoteBuf) > 0 {
			resultLines = append(resultLines, "<blockquote>"+strings.Join(quoteBuf, "\n")+"</blockquote>")
			quoteBuf = nil
		}
	}
	for _, line := range lines {
		stripped := strings.TrimLeftFunc(line, unicode.IsSpace)
		if strings.HasPrefix(stripped, "&gt; ") {
			quoteBuf = append(quoteBuf, stripped[5:])
		} else if stripped == "&gt;" {
			quoteBuf = append(quoteBuf, "")
		} else {
			flushQuote()
			resultLines = append(resultLines, line)
		}
	}
	flushQuote()
	text = strings.Join(resultLines, "\n")

	text = bulletRe.ReplaceAllString(text, "${1}• ")
	text = spoilerRe.ReplaceAllString(text, "<tg-spoiler>${1}</tg-spoiler>")
	text = boldItalicRe.ReplaceAllString(text, "<b><i>${1}</i></b>")
	text = boldRe.ReplaceAllString(text, "<b>${1}</b>")
	text = boldUnderlineRe.ReplaceAllString(text, "<b>${1}</b>")

	// Python used lookaround: (?<!\w)\*(.+?)\*(?!\w) and the _ variant.
	// RE2 has no lookaround, so these two are emulated with a manual
	// scanner that reproduces the backtracking semantics.
	text = replaceWrappedWordSafe(text, "*", func(inner string) string {
		return "<i>" + inner + "</i>"
	})
	text = replaceWrappedWordSafe(text, "_", func(inner string) string {
		return "<i>" + inner + "</i>"
	})
	text = strikeRe.ReplaceAllString(text, "<s>${1}</s>")

	for idx, content := range placeholders {
		text = strings.ReplaceAll(text, "\x00PH"+strconv.Itoa(idx)+"\x00", content)
	}
	return text
}

// isWordRune mirrors Python re \w for str patterns: alphanumeric (Unicode
// letters and digits) plus underscore.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// replaceWrappedWordSafe emulates re.sub(r"(?<!\w)D(.+?)D(?!\w)", repl, text)
// for a literal delimiter D without lookaround support:
//
//   - the character before the opening delimiter must not be a word char;
//   - the inner group `.+?` is non-greedy (first valid closing wins) and
//     cannot span a newline (`.` excludes \n) and needs at least one char;
//   - the character after the closing delimiter must not be a word char —
//     when it is, the scan backtracks by extending the inner group to the
//     next delimiter occurrence;
//   - when no closing delimiter works, matching resumes after the opening
//     delimiter.
func replaceWrappedWordSafe(text, delim string, repl func(string) string) string {
	if text == "" || delim == "" {
		return text
	}
	var sb strings.Builder
	writePos := 0 // everything before this is already emitted
	scanPos := 0  // next candidate position
	for {
		idx := strings.Index(text[scanPos:], delim)
		if idx < 0 {
			break
		}
		open := scanPos + idx
		if open > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:open])
			if isWordRune(r) {
				scanPos = open + 1
				continue
			}
		}
		innerStart := open + len(delim)
		// `.+?` cannot contain \n, so the closing delimiter must appear
		// before the first newline at/after innerStart.
		searchEnd := len(text)
		if nl := strings.IndexByte(text[innerStart:], '\n'); nl >= 0 {
			searchEnd = innerStart + nl
		}
		closed := -1
		c := innerStart
		for c < searchEnd {
			j := strings.Index(text[c:searchEnd], delim)
			if j < 0 {
				break
			}
			cand := c + j
			if cand > innerStart { // inner needs at least one char
				after := cand + len(delim)
				ok := true
				if after < len(text) {
					r, _ := utf8.DecodeRuneInString(text[after:])
					if isWordRune(r) {
						ok = false // (?!\w) failed — extend the inner group
					}
				}
				if ok {
					closed = cand
					break
				}
			}
			c = cand + 1
		}
		if closed < 0 {
			scanPos = open + 1
			continue
		}
		sb.WriteString(text[writePos:open])
		sb.WriteString(repl(text[innerStart:closed]))
		writePos = closed + len(delim)
		scanPos = writePos
	}
	sb.WriteString(text[writePos:])
	return sb.String()
}
