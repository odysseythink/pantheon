package openaicompat

import "github.com/odysseythink/pantheon/core"

// Hooks allow providers to customize the behavior of the OpenAI-compatible
// client at key points in the request/response lifecycle.
type Hooks struct {
	// PrepareRequest is called after the ChatCompletionRequest is built but
	// before it is sent. Use this to inject provider-specific fields from
	// core.Request.ProviderOptions or to modify any request parameter.
	PrepareRequest func(req *ChatCompletionRequest, model string, coreReq *core.Request)

	// MapFinishReason maps the raw finish reason string to the core format.
	// If nil, the raw string is passed through unchanged.
	MapFinishReason func(string) string

	// PostProcessResponse is called after the raw response is converted to
	// core.Response. Use this to modify or enrich the final response.
	PostProcessResponse func(resp *core.Response, raw *ChatCompletionResponse)

	// PostProcessStreamPart is called for each stream part before it is yielded.
	// Use this to modify, filter, or inject additional stream parts.
	PostProcessStreamPart func(part *core.StreamPart, raw *ChatCompletionResponse)
}
