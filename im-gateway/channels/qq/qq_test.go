package qq

// 语义对照基准：octop-gateway Python 原版
//   - extractThinkBlocks  -> src/octop_gateway/channels/qq/channel.py:113 extract_think_blocks
//   - stripThinkTags      -> src/octop_gateway/channels/qq/channel.py:121 strip_think_tags
//   - prefixMatches       -> src/octop_gateway/channels/qq/stream.py:48 prefix_matches
//   - stableMarkdownPrefix-> src/octop_gateway/channels/qq/stream_blocks.py:27 stable_markdown_prefix
//
// 所有期望值均按 Python 源语义（含正则 finditer / sub 的非贪婪、DOTALL、
// 忽略大小写行为）逐行推演得出。

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// extractThinkBlocks
// ---------------------------------------------------------------------------

func TestExtractThinkBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"simple think", "<think>abc</think>", "abc"},
		{"thinking variant", "<thinking>abc</thinking>", "abc"},
		{"case insensitive", "<THINK>abc</THINK>", "abc"},
		{
			"multiple blocks joined by newline",
			"<think>a</think>mid<think>b</think>",
			"a\nb",
		},
		{
			"whitespace-only block skipped, others trimmed",
			"<think>   </think><think> b </think>",
			"b",
		},
		{"unclosed block ignored", "<think>abc", ""},
		{
			// Python finditer 非贪婪：第一段匹配到第一个 </think>，
			// 剩余 "c</think>" 无 opener，不再匹配。
			"nested think",
			"<think>a<think>b</think>c</think>",
			"a<think>b",
		},
		{
			// re.DOTALL：跨行内容整体匹配，仅 strip 首尾。
			"multiline inner kept",
			"<think>line1\nline2</think>",
			"line1\nline2",
		},
		{"no tags", "plain text", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractThinkBlocks(tc.in); got != tc.want {
				t.Errorf("extractThinkBlocks(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stripThinkTags
// ---------------------------------------------------------------------------

func TestStripThinkTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"leading block", "<think>a</think>hello", "hello"},
		{"trailing block", "hello<think>a</think>", "hello"},
		{"surrounded", "before<think>x</think>after", "beforeafter"},
		{"no tags kept as-is", "plain text", "plain text"},
		{
			// Python 语义：完整块被 sub 掉，保留两侧换行（不去空白）。
			"surrounding whitespace kept",
			"a\n<think>x</think>\nb",
			"a\n\nb",
		},
		{
			// 只有 close：循环发现 close 且无更早 open，切掉 close 及其之前内容。
			"only closing tag", "a</think>b", "b",
		},
		{
			// 只有 open：末尾 open 之后全部丢弃。
			"only opening tag", "<think>abc", "",
		},
		{
			// close 在前 open 在后：先切 close 前，再切 open 后。
			"close before open", "</think>open<think>", "open",
		},
		{
			// Python sub 只移除第一个非贪婪块 "…<think>b</think>"，
			// 剩余 "c</think>tail" 中 close 前无 open → 切掉 close 及其前，
			// 得 "tail"。
			"nested tags keep tail",
			"<think>a<think>b</think>c</think>tail",
			"tail",
		},
		{"mixed case close only", "x</THINK>y", "y"},
		{"mixed case open only", "<Thinking>z", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripThinkTags(tc.in); got != tc.want {
				t.Errorf("stripThinkTags(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// prefixMatches
// ---------------------------------------------------------------------------

func TestPrefixMatches(t *testing.T) {
	cases := []struct {
		name     string
		accepted string
		incoming string
		want     bool
	}{
		{"incoming extends accepted", "hello", "hello world", true},
		{"incoming equals accepted", "hello", "hello", true},
		{"incoming shorter", "hello world", "hello", false},
		{"empty accepted", "", "anything", true},
		{"newline hold, empty incoming", "\n", "", false},
		{"newline hold, incoming keeps newline", "\n", "\nhello", true},
		{"leading newline dropped", "\nfoo", "foo", false},
		{"leading crlf dropped", "\r\nfoo", "foo", false},
		{"leading newline kept", "\nfoo", "\nfoo bar", true},
		{"whitespace tolerant extra spaces", "hello world", "hello  world", true},
		{"whitespace tolerant fewer spaces", "hello  world", "hello world", true},
		{"leading space collapsed", " foo", "foo", true},
		{"newline collapsed to space", "a\nb", "a b", true},
		{"not a prefix", "hello", "hell", false},
		{"case sensitive", "Hello", "hello", false},
		{"unicode prefix", "你好", "你好呀", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prefixMatches(tc.accepted, tc.incoming); got != tc.want {
				t.Errorf("prefixMatches(%q, %q) = %v, want %v",
					tc.accepted, tc.incoming, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// stableMarkdownPrefix
// ---------------------------------------------------------------------------

func TestStableMarkdownPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no complete line", "para", ""},
		{
			// Python _consume_paragraph 跑到末尾返回 None → 段落不落定。
			"single paragraph line not stable", "para\n", "",
		},
		{
			// 段落后跟空行 → 空行也在稳定前缀内。
			"paragraph settled by blank line",
			"para1\npara2\n\nrest",
			"para1\npara2\n\n",
		},
		{"heading then paragraph", "# Title\nbody\n\nmore", "# Title\nbody\n\n"},
		{
			// 围栏闭合即稳定，其后未完段落不计。
			"closed fence",
			"```py\ncode\n```\nafter\n",
			"```py\ncode\n```\n",
		},
		{"unclosed fence held", "```py\ncode line\n", ""},
		{"tilde fence closed", "~~~\ncode\n~~~\nafter\n", "~~~\ncode\n~~~\n"},
		{
			// 表格需要 header+sep+至少一个数据行才落定。
			"incomplete table held", "|a|b|\n|-|-|\nrest", "",
		},
		{
			"complete table one row",
			"|a|b|\n|-|-|\n|1|2|\nrest",
			"|a|b|\n|-|-|\n|1|2|\n",
		},
		{
			// 后续数据行逐行扩展稳定前缀。
			"complete table multiple rows",
			"|a|b|\n|-|-|\n|1|2|\n|3|4|\n|5|6|\ntrailing",
			"|a|b|\n|-|-|\n|1|2|\n|3|4|\n|5|6|\n",
		},
		{
			// 紧凑列表项吞并缩进续行，"next" 为未完段落不计。
			"list item with continuation", "- item\n  cont\nnext", "- item\n  cont\n",
		},
		{"adjacent list items", "- a\n- b\n", "- a\n- b\n"},
		{"blockquote then paragraph", "> q\n> q2\nafter\n\n", "> q\n> q2\nafter\n\n"},
		{
			"indented code then paragraph",
			"    code\n    more\nrest\n\n",
			"    code\n    more\nrest\n\n",
		},
		{"thematic break", "---\nnext\n\n", "---\nnext\n\n"},
		{"crlf normalized", "# H\r\nbody\r\n\r\n", "# H\nbody\n\n"},
		{"blank lines only", "\n\n", "\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stableMarkdownPrefix(tc.in); got != tc.want {
				t.Errorf("stableMarkdownPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestStableMarkdownPrefixMonotonic 验证增长文本下前缀单调不减。
func TestStableMarkdownPrefixMonotonic(t *testing.T) {
	steps := []string{
		"# H",
		"# H\n",
		"# H\npara",
		"# H\npara\n",
		"# H\npara\n\n",
		"# H\npara\n\n- item",
		"# H\npara\n\n- item\n  cont\n",
	}
	prev := ""
	for i, s := range steps {
		got := stableMarkdownPrefix(s)
		if !strings.HasPrefix(got, prev) {
			t.Fatalf("step %d: prefix shrank: prev=%q got=%q", i, prev, got)
		}
		prev = got
	}
}
