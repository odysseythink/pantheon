// Package qq ports octop_gateway.channels.qq: the QQ Bot channel via the
// official bot WebSocket gateway and REST API.
//
// This file ports stream_blocks.py — stable Markdown prefixes for QQ C2C
// replace-mode streaming.
//
// The official stream merge only appends the unsent suffix onto a locked
// prefix. A "\n" that lands on that splice is often dropped, which breaks
// tables and other block constructs. stableMarkdownPrefix offers the longest
// prefix that ends on a complete block so a later frame's suffix does not
// start with a stray newline inside a table / fence / list item.
//
// The parser is conservative and monotonic: growing text never shrinks the
// returned prefix. Incomplete trailing content is omitted until finish sends
// the full answer.
package qq

import (
	"regexp"
	"strings"
)

var (
	fenceRe        = regexp.MustCompile(`^( {0,3})(` + "`{3,}" + `|~{3,})(.*)$`)
	atxRe          = regexp.MustCompile(`^ {0,3}#{1,6}(?:\s|$)`)
	hrRe           = regexp.MustCompile(`^ {0,3}(?:(?:\*(?:\s*\*){2,})|(?:-(?:\s*-){2,})|(?:_(?:\s*_){2,}))\s*$`)
	listRe         = regexp.MustCompile(`^ {0,3}(?:[*+-]|\d{1,9}[.)])(?:\s+|$)`)
	quoteRe        = regexp.MustCompile(`^ {0,3}>`)
	tableSepCellRe = regexp.MustCompile(`^:?-+:?$`)
	indentRe       = regexp.MustCompile(`^(?: {4,}|\t)`)
)

// stableMarkdownPrefix returns the longest complete-block prefix of text.
//
// Recognizes ATX headings, fenced code, GFM tables, lists, block quotes,
// thematic breaks, indented code, and paragraphs. A table is held until
// its header, separator, and at least one full data row are present;
// later full rows extend the prefix one row at a time.
func stableMarkdownPrefix(text string) string {
	if text == "" {
		return ""
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	complete, _ := splitCompleteLines(normalized)
	end := stableLineEnd(complete)
	if end <= 0 {
		return ""
	}
	return strings.Join(complete[:end], "\n") + "\n"
}

func splitCompleteLines(text string) ([]string, string) {
	if !strings.Contains(text, "\n") {
		return nil, text
	}
	if strings.HasSuffix(text, "\n") {
		return strings.Split(text[:len(text)-1], "\n"), ""
	}
	parts := strings.Split(text, "\n")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

func stableLineEnd(lines []string) int {
	index := 0
	stable := 0
	for index < len(lines) {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			index++
			stable = index
			continue
		}

		fence := fenceRe.FindStringSubmatch(line)
		if fence != nil {
			closer := findFenceClose(lines, index, fence)
			if closer < 0 {
				break
			}
			index = closer + 1
			stable = index
			continue
		}

		if atxRe.MatchString(line) {
			index++
			stable = index
			continue
		}

		if isThematicBreak(line) {
			index++
			stable = index
			continue
		}

		tableEnd, tableStarted := tryConsumeTable(lines, index)
		if tableStarted {
			if tableEnd == 0 {
				break
			}
			index = tableEnd
			stable = index
			continue
		}

		if listRe.MatchString(line) {
			index = consumeTightItem(lines, index)
			stable = index
			continue
		}

		if quoteRe.MatchString(line) {
			index = consumeBlockquote(lines, index)
			stable = index
			continue
		}

		if indentRe.MatchString(line) {
			index = consumeIndentedCode(lines, index)
			stable = index
			continue
		}

		paraEnd, ok := consumeParagraph(lines, index)
		if !ok {
			break
		}
		index = paraEnd
		stable = index
	}
	return stable
}

// findFenceClose returns the index of the fence closer, or -1 when unclosed.
func findFenceClose(lines []string, start int, fence []string) int {
	marker := fence[2][:1]
	minLen := len(fence[2])
	closer := regexp.MustCompile(`^ {0,3}` + regexp.QuoteMeta(marker) + `{` + itoa(minLen) + `,}\s*$`)
	for index := start + 1; index < len(lines); index++ {
		if closer.MatchString(lines[index]) {
			return index
		}
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func isThematicBreak(line string) bool {
	if strings.Contains(line, "|") {
		return false
	}
	return hrRe.MatchString(line)
}

func isTableRow(line string) bool {
	stripped := strings.TrimSpace(line)
	if !strings.Contains(stripped, "|") {
		return false
	}
	if isThematicBreak(stripped) {
		return false
	}
	return strings.Count(stripped, "|") >= 1 && !atxRe.MatchString(stripped)
}

func isTableSeparator(line string) bool {
	stripped := strings.TrimSpace(line)
	if !strings.Contains(stripped, "|") {
		return false
	}
	body := strings.Trim(stripped, "|")
	cells := strings.Split(body, "|")
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		if !tableSepCellRe.MatchString(cell) {
			return false
		}
	}
	return true
}

// tryConsumeTable returns (exclusive end index, true) when a complete table
// was consumed, (0, true) when a table started but is incomplete, and
// (0, false) when no table starts at this line.
func tryConsumeTable(lines []string, start int) (int, bool) {
	if !isTableRow(lines[start]) || isTableSeparator(lines[start]) {
		return 0, false
	}
	if start+1 >= len(lines) {
		return 0, true
	}
	if !isTableSeparator(lines[start+1]) {
		return 0, false
	}
	index := start + 2
	if index >= len(lines) || !isTableRow(lines[index]) || isTableSeparator(lines[index]) {
		return 0, true
	}
	index++
	for index < len(lines) && isTableRow(lines[index]) && !isTableSeparator(lines[index]) {
		index++
	}
	return index, true
}

func consumeTightItem(lines []string, start int) int {
	index := start + 1
	for index < len(lines) {
		nxt := lines[index]
		if strings.TrimSpace(nxt) == "" {
			break
		}
		if isBlockStart(nxt) && !indentRe.MatchString(nxt) {
			break
		}
		if listRe.MatchString(nxt) {
			break
		}
		if strings.HasPrefix(nxt, " ") || strings.HasPrefix(nxt, "\t") {
			index++
			continue
		}
		break
	}
	return index
}

func consumeBlockquote(lines []string, start int) int {
	index := start + 1
	for index < len(lines) {
		nxt := lines[index]
		if strings.TrimSpace(nxt) == "" {
			break
		}
		if quoteRe.MatchString(nxt) || strings.HasPrefix(nxt, "  ") || strings.HasPrefix(nxt, "\t") {
			index++
			continue
		}
		break
	}
	return index
}

func consumeIndentedCode(lines []string, start int) int {
	index := start + 1
	for index < len(lines) && (strings.TrimSpace(lines[index]) == "" || indentRe.MatchString(lines[index])) {
		if strings.TrimSpace(lines[index]) == "" && index+1 < len(lines) && !indentRe.MatchString(lines[index+1]) {
			break
		}
		index++
	}
	return index
}

func isBlockStart(line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	return fenceRe.MatchString(line) ||
		atxRe.MatchString(line) ||
		isThematicBreak(line) ||
		listRe.MatchString(line) ||
		quoteRe.MatchString(line)
}

// consumeParagraph returns (end index, true); ok=false mirrors the Python
// None return (paragraph ran to the end of the available lines).
func consumeParagraph(lines []string, start int) (int, bool) {
	index := start + 1
	for index < len(lines) && !isBlockStart(lines[index]) {
		if looksLikeTableOpen(lines, index) {
			break
		}
		index++
	}
	if index >= len(lines) {
		return 0, false
	}
	if strings.TrimSpace(lines[index]) == "" {
		return index + 1, true
	}
	return index, true
}

func looksLikeTableOpen(lines []string, index int) bool {
	if !isTableRow(lines[index]) {
		return false
	}
	if index+1 < len(lines) && isTableSeparator(lines[index+1]) {
		return true
	}
	return index+1 >= len(lines)
}
