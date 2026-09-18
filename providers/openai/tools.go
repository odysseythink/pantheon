package openai

import "github.com/odysseythink/pantheon/core"

// WebSearchTool returns a provider-defined tool for OpenAI's web search.
func WebSearchTool() core.ToolDefinition {
	return core.ToolDefinition{
		Name: "web_search",
		ProviderTool: &core.ProviderDefinedTool{
			ID:   "openai.web_search_preview",
			Name: "web_search",
		},
	}
}
