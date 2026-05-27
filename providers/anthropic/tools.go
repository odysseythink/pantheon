package anthropic

import "github.com/odysseythink/pantheon/core"

// WebSearchTool returns a provider-defined tool for Anthropic's web search.
func WebSearchTool() core.ToolDefinition {
	return core.ToolDefinition{
		Name: "web_search",
		ProviderTool: &core.ProviderDefinedTool{
			ID:   "anthropic.web_search",
			Name: "web_search",
		},
	}
}
