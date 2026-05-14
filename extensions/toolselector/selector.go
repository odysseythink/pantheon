package toolselector

import (
	"strings"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

// defaultToolsetKeywords maps each toolset to the keywords that trigger its
// inclusion when dynamic tool selection is enabled.
var defaultToolsetKeywords = map[string][]string{
	"web":      {"search", "搜索", "查找", "查", "google", "bing", "fetch", "抓取", "网页", "url"},
	"terminal": {"run", "execute", "shell", "bash", "command", "cmd", "执行", "运行", "命令", "终端"},
	"file":     {"file", "files", "目录", "文件夹", "读取", "写", "编辑", "保存", "打开", "查看", "ls", "dir"},
	"obsidian": {"obsidian", "note", "笔记", "vault", "front matter", "link"},
	"memory":   {"memory", "记住", "记忆", "memo", "recall", "remember"},
	"vision":   {"image", "picture", "photo", "图", "图片", "照片", "看图", "识别"},
	"chart":    {"chart", "graph", "plot", "图表", "画图", "可视化", "统计"},
	"delegate": {"delegate", "subagent", "子任务", "分派", "代理", "agent"},
}

// coreToolsets are always included regardless of the user query.
var coreToolsets = map[string]struct{}{
	"web":      {},
	"file":     {},
	"terminal": {},
}

// ToolSelector filters the full tool registry down to the subset most likely
// needed for the current user request. When dynamic selection is enabled the
// engine calls it every turn before building the provider request.
type ToolSelector struct {
	keywords map[string][]string
}

// NewToolSelector creates a selector with the built-in keyword map.
func NewToolSelector() *ToolSelector {
	return &ToolSelector{keywords: defaultToolsetKeywords}
}

// Select returns the tool definitions that should be sent to the LLM for the
// current turn. It always includes core toolsets, adds toolsets matched by the
// latest user message, and preserves toolsets that were used in previous turns
// (so multi-turn tool chains do not break).
func (s *ToolSelector) Select(userQuery string, history []core.Message, reg *tool.Registry) []core.ToolDefinition {
	selected := make(map[string]struct{})

	// 1. Core toolsets are always available.
	for ts := range coreToolsets {
		selected[ts] = struct{}{}
	}

	// 2. Match the latest user query against toolset keywords.
	lower := strings.ToLower(userQuery)
	for toolset, kws := range s.keywords {
		for _, kw := range kws {
			if strings.Contains(lower, kw) {
				selected[toolset] = struct{}{}
				break
			}
		}
	}

	// 3. Preserve toolsets used in previous turns (multi-turn chains).
	for _, m := range history {
		for _, p := range m.Content {
			call, ok := p.(core.ToolCallPart)
			if !ok {
				continue
			}
			for _, e := range reg.Entries(nil) {
				if e.Name == call.Name {
					selected[e.Toolset] = struct{}{}
					break
				}
			}
		}
	}

	// 4. Build the filtered definitions.
	return reg.Definitions(func(e *tool.Entry) bool {
		_, ok := selected[e.Toolset]
		return ok
	})
}
