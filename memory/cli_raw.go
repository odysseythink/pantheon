package memory

// raw CLI command group, ported from adapters/cli/raw_cmd.py (L0 events).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// cliEventTypes mirrors RawEventType get_args.
var cliEventTypes = []string{
	string(RawEventUserMessage), string(RawEventAssistantMessage),
	string(RawEventToolCall), string(RawEventToolResult),
	string(RawEventHostMemoryWrite), string(RawEventSessionStart),
	string(RawEventSessionEnd), string(RawEventCompaction), string(RawEventManual),
}

const cliSnippetMaxLen = 80

// cliSnippet truncates content to max runes ("..." suffix), as Python does
// with code-point slicing.
func cliSnippet(content string, max int) string {
	if cliUTF8Len2(content) <= max {
		return content
	}
	runes := []rune(content)
	return string(runes[:max-3]) + "..."
}

func cliUTF8Len2(s string) int { return len([]rune(s)) }

func cliRawDict(e *RawEvent) []cliPair {
	return ord(
		cliPair{"id", e.ID},
		cliPair{"host", e.Host},
		cliPair{"session_id", cliStrPtr(e.SessionID)},
		cliPair{"thread_id", cliStrPtr(e.ThreadID)},
		cliPair{"user", cliStrPtr(e.User)},
		cliPair{"timestamp", pyISOFormat(e.Timestamp)},
		cliPair{"event_type", string(e.EventType)},
		cliPair{"content", e.Content},
		cliPair{"payload", cliMetaDict(e.Payload)},
	)
}

// cliParsePayload parses --payload JSON (must be an object).
func cliParsePayload(raw any) (map[string]any, error) {
	s, ok := raw.(string)
	if !ok || s == "" {
		return nil, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, &cliUsageError{fmt.Sprintf("--payload must be valid JSON: %v", err)}
	}
	md, ok := parsed.(map[string]any)
	if !ok {
		return nil, &cliUsageError{"--payload must be a JSON object (got array/scalar)"}
	}
	return md, nil
}

// cliParseISO parses an optional ISO 8601 datetime (datetime.fromisoformat).
func cliParseISO(raw any, flagName string) (*time.Time, error) {
	s, ok := raw.(string)
	if !ok || s == "" {
		return nil, nil
	}
	t, err := parseFlexibleISO(s)
	if err != nil {
		return nil, &cliUsageError{fmt.Sprintf("invalid ISO 8601 datetime: %s", pyReprScalar(s))}
	}
	return &t, nil
}

func cliRawGroup() *cliGroup {
	return &cliGroup{
		name: "raw",
		help: "Inspect / manage L0 raw events.",
		commands: []*cliCommand{
			{
				name: "add",
				help: "Append a raw event (mostly for testing / manual ingest).",
				options: []cliOption{
					{name: "content", required: true, help: "Event content, or '-' to read from stdin."},
					{name: "event-type", def: "manual", choices: cliEventTypes},
					{name: "host", def: "manual"},
					{name: "session-id"},
					{name: "thread-id"},
					{name: "user"},
					{name: "timestamp", help: "ISO 8601 datetime; defaults to now(UTC)."},
					{name: "payload", help: `Payload as JSON object, e.g. '{"role":"user"}'.`},
				},
				run: cliRawAdd,
			},
			{
				name: "show",
				help: "Show a raw event by id.",
				args: []cliArg{{name: "EVENT_ID"}},
				run:  cliRawShow,
			},
			{
				name: "list",
				help: "List raw events (most recent first).",
				options: []cliOption{
					{name: "host"},
					{name: "session-id"},
					{name: "thread-id"},
					{name: "user"},
					{name: "event-type", choices: cliEventTypes},
					{name: "after", help: "ISO 8601 datetime, inclusive."},
					{name: "before", help: "ISO 8601 datetime, inclusive."},
					{name: "limit", def: 20, isInt: true},
				},
				run: cliRawList,
			},
			{
				name: "search",
				help: "FTS search over raw event content.",
				args: []cliArg{{name: "QUERY"}},
				options: []cliOption{
					{name: "limit", def: 10, isInt: true},
				},
				run: cliRawSearch,
			},
		},
	}
}

func cliRawAdd(c *cliContext, opts map[string]any, args []string) error {
	content := opts["content"].(string)
	if content == "-" {
		content = c.readAll()
	}
	payload, err := cliParsePayload(opts["payload"])
	if err != nil {
		return err
	}
	timestamp, err := cliParseISO(opts["timestamp"], "timestamp")
	if err != nil {
		return err
	}
	eventType := RawEventManual
	if et, _ := opts["event-type"].(string); et != "" {
		eventType = RawEventType(et)
	}

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	var addOpts []AddRawOption
	addOpts = append(addOpts, WithRawHost(opts["host"].(string)))
	if v, _ := opts["session-id"].(string); v != "" {
		addOpts = append(addOpts, WithRawSession(v))
	}
	if v, _ := opts["thread-id"].(string); v != "" {
		addOpts = append(addOpts, WithRawThread(v))
	}
	if v, _ := opts["user"].(string); v != "" {
		addOpts = append(addOpts, WithRawUser(v))
	}
	if timestamp != nil {
		addOpts = append(addOpts, WithRawTimestamp(*timestamp))
	}
	if payload != nil {
		addOpts = append(addOpts, WithRawPayload(payload))
	}
	event, err := m.AddRaw(context.Background(), content, eventType, addOpts...)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		cliOK(c, cliRawDict(event))
		return nil
	}
	c.echo(fmt.Sprintf("Added raw event: %s", event.ID))
	return nil
}

func cliRawShow(c *cliContext, opts map[string]any, args []string) error {
	eventID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	event, err := m.GetRaw(context.Background(), eventID)
	if err != nil || event == nil {
		if c.optBool("output_json") {
			c.echo(pyJSON(ord(cliPair{"status", "error"}, cliPair{"error", "not found"}), true, ""))
		} else {
			c.echoErr(fmt.Sprintf("Raw event not found: %s", eventID))
		}
		return errCLIExit1
	}

	if c.optBool("output_json") {
		cliOK(c, cliRawDict(event))
		return nil
	}
	c.echo(fmt.Sprintf("[%s] %s / %s", pyISOFormat(event.Timestamp), event.Host, event.EventType))
	c.echo(fmt.Sprintf("  id=%s", event.ID))
	if event.SessionID != nil {
		c.echo(fmt.Sprintf("  session=%s", *event.SessionID))
	}
	if event.User != nil {
		c.echo(fmt.Sprintf("  user=%s", *event.User))
	}
	c.echo(fmt.Sprintf("  content: %s", event.Content))
	if event.Payload != nil && len(event.Payload) > 0 {
		c.echo(fmt.Sprintf("  payload: %s", pyJSON(event.Payload, false, "")))
	}
	return nil
}

func cliRawList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	filter := RawEventFilter{Limit: opts["limit"].(int)}
	if v, _ := opts["host"].(string); v != "" {
		filter.Host = &v
	}
	if v, _ := opts["session-id"].(string); v != "" {
		filter.SessionID = &v
	}
	if v, _ := opts["thread-id"].(string); v != "" {
		filter.ThreadID = &v
	}
	if v, _ := opts["user"].(string); v != "" {
		filter.User = &v
	}
	if v, _ := opts["event-type"].(string); v != "" {
		et := RawEventType(v)
		filter.EventType = &et
	}
	if filter.After, err = cliParseISO(opts["after"], "after"); err != nil {
		return err
	}
	if filter.Before, err = cliParseISO(opts["before"], "before"); err != nil {
		return err
	}

	events, err := m.ListRaw(context.Background(), filter)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(events))
		for _, e := range events {
			dicts = append(dicts, cliRawDict(e))
		}
		cliOK(c, ord(cliPair{"events", dicts}))
		return nil
	}
	if len(events) == 0 {
		c.echo("(no events)")
		return nil
	}
	for _, e := range events {
		snippet := cliSnippet(e.Content, cliSnippetMaxLen)
		c.echo(fmt.Sprintf("%s  %-8s  %-18s  %s", pyISOFormat(e.Timestamp), e.Host, e.EventType, snippet))
		c.echo(fmt.Sprintf("  id=%s", e.ID))
	}
	return nil
}

func cliRawSearch(c *cliContext, opts map[string]any, args []string) error {
	query := args[0]
	limit, _ := opts["limit"].(int)
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	events, err := m.SearchRaw(context.Background(), query, limit)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(events))
		for _, e := range events {
			dicts = append(dicts, cliRawDict(e))
		}
		cliOK(c, ord(cliPair{"events", dicts}))
		return nil
	}
	if len(events) == 0 {
		c.echo("(no results)")
		return nil
	}
	for _, e := range events {
		snippet := cliSnippet(e.Content, cliSnippetMaxLen)
		c.echo(fmt.Sprintf("%s  %-8s  %s", pyISOFormat(e.Timestamp), e.Host, snippet))
		c.echo(fmt.Sprintf("  id=%s", e.ID))
	}
	return nil
}
