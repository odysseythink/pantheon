package memory

// Memory CLI command group, ported from adapters/cli/memory_cmd.py.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

var cliLevelChoices = []string{string(NodeLevelRoot), string(NodeLevelBranch), string(NodeLevelLeaf)}

// cliNodeDict renders a MemoryNode in _node_to_dict key order
// (created_at/updated_at via datetime.isoformat()).
func cliNodeDict(n *MemoryNode) []cliPair {
	return ord(
		cliPair{"id", n.ID},
		cliPair{"parent_id", cliStrPtr(n.ParentID)},
		cliPair{"level", string(n.Level)},
		cliPair{"content", n.Content},
		cliPair{"topic", cliStrPtr(n.Topic)},
		cliPair{"conversation_id", cliStrPtr(n.ConversationID)},
		cliPair{"created_at", pyISOFormat(n.CreatedAt)},
		cliPair{"updated_at", pyISOFormat(n.UpdatedAt)},
		cliPair{"metadata", cliMetaDict(n.Metadata)},
	)
}

func cliStrPtr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// cliMetaDict renders metadata: Python dataclass default {} (never null).
func cliMetaDict(md map[string]any) any {
	if md == nil {
		return []cliPair{}
	}
	return md
}

// cliParseMetadata parses --metadata JSON; must be a JSON object.
func cliParseMetadata(raw any) (map[string]any, error) {
	s, ok := raw.(string)
	if !ok || s == "" {
		return nil, nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil, &cliUsageError{fmt.Sprintf("--metadata must be valid JSON: %v", err)}
	}
	md, ok := parsed.(map[string]any)
	if !ok {
		return nil, &cliUsageError{"--metadata must be a JSON object (got array/scalar)"}
	}
	return md, nil
}

func cliOK(c *cliContext, data any) {
	c.echo(pyJSON(ord(cliPair{"status", "ok"}, cliPair{"data", data}), true, ""))
}

// cliExitNotFound prints the not-found envelope/text and mirrors ctx.exit(1)
// (both the JSON and text branches exit 1 in Python).
func cliExitNotFound(c *cliContext, what string) error {
	if c.optBool("output_json") {
		c.echo(pyJSON(ord(cliPair{"status", "error"}, cliPair{"error", "not found"}), true, ""))
	} else {
		c.echoErr(fmt.Sprintf("Node not found: %s", what))
	}
	return errCLIExit1
}

func cliMemoryGroup() *cliGroup {
	return &cliGroup{
		name: "memory",
		help: "Manage memory nodes.",
		commands: []*cliCommand{
			{
				name: "store",
				help: "Store a new memory node.",
				options: []cliOption{
					{name: "content", required: true, help: "Memory content, or '-' to read from stdin."},
					{name: "topic", help: "Topic or category for this memory."},
					{name: "level", def: "leaf", choices: cliLevelChoices, help: "Node level in the memory tree."},
					{name: "parent", help: "Parent node ID."},
					{name: "metadata", help: `Metadata as a JSON object, e.g. '{"confidence":"high"}'.`},
				},
				run: cliMemoryStore,
			},
			{
				name: "get",
				help: "Fetch a memory node by id.",
				args: []cliArg{{name: "NODE_ID"}},
				run:  cliMemoryGet,
			},
			{
				name: "update",
				help: "Update an existing memory node's mutable fields.",
				args: []cliArg{{name: "NODE_ID"}},
				options: []cliOption{
					{name: "content", help: "New content."},
					{name: "topic", help: "New topic. Pass empty string to clear."},
					{name: "metadata", help: "New metadata JSON object (REPLACE semantics, not merge)."},
				},
				run: cliMemoryUpdate,
			},
			{
				name: "delete",
				help: "Delete a memory node. Refuses if it has children unless --cascade.",
				args: []cliArg{{name: "NODE_ID"}},
				options: []cliOption{
					{name: "cascade", flag: true, def: false, help: "Recursively delete subtree."},
				},
				run: cliMemoryDelete,
			},
			{
				name: "tree",
				help: "Show the full memory tree.",
				run:  cliMemoryTree,
			},
			{
				name: "recall",
				help: "Recall memories matching a query.",
				args: []cliArg{{name: "QUERY"}},
				options: []cliOption{
					{name: "limit", def: 5, isInt: true, help: "Maximum results."},
				},
				run: cliMemoryRecall,
			},
		},
	}
}

func cliMemoryStore(c *cliContext, opts map[string]any, args []string) error {
	content := opts["content"].(string)
	if content == "-" {
		content = c.readAll()
	}
	metadata, err := cliParseMetadata(opts["metadata"])
	if err != nil {
		return err
	}

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	var storeOpts []StoreOption
	if topic, _ := opts["topic"].(string); topic != "" {
		storeOpts = append(storeOpts, WithStoreTopic(topic))
	}
	if level, _ := opts["level"].(string); level != "" && level != "leaf" {
		storeOpts = append(storeOpts, WithStoreLevel(NodeLevel(level)))
	}
	if parent, _ := opts["parent"].(string); parent != "" {
		storeOpts = append(storeOpts, WithStoreParent(parent))
	}
	if metadata != nil {
		storeOpts = append(storeOpts, WithStoreMetadata(metadata))
	}
	node, err := m.Store(context.Background(), content, storeOpts...)
	if err != nil {
		// Python catches ValueError → click.ClickException (exit 1).
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		cliOK(c, cliNodeDict(node))
		return nil
	}
	c.echo(fmt.Sprintf("Stored memory node: %s", node.ID))
	return nil
}

func cliMemoryGet(c *cliContext, opts map[string]any, args []string) error {
	nodeID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	node, err := m.Get(context.Background(), nodeID)
	if err != nil || node == nil {
		return cliExitNotFound(c, nodeID)
	}

	if c.optBool("output_json") {
		cliOK(c, cliNodeDict(node))
		return nil
	}
	topicStr := ""
	if node.Topic != nil && *node.Topic != "" {
		topicStr = fmt.Sprintf(" [%s]", *node.Topic)
	}
	c.echo(fmt.Sprintf("%s: %s%s", node.Level, node.Content, topicStr))
	c.echo(fmt.Sprintf("  id=%s", node.ID))
	if node.ParentID != nil {
		c.echo(fmt.Sprintf("  parent=%s", *node.ParentID))
	}
	if node.Metadata != nil && len(node.Metadata) > 0 {
		// Python: json.dumps(node.metadata, ensure_ascii=False)
		c.echo(fmt.Sprintf("  metadata=%s", pyJSON(node.Metadata, false, "")))
	}
	return nil
}

func cliMemoryUpdate(c *cliContext, opts map[string]any, args []string) error {
	nodeID := args[0]
	content, hasContent := opts["content"].(string)
	topicRaw, hasTopic := opts["topic"].(string)
	// Metadata is pre-filled as nil when absent — judge the value, not the
	// key presence.
	metadataRaw := opts["metadata"]
	hasMetadata := metadataRaw != nil

	// _UNSET semantics: option absent → leave unchanged; --topic "" → clear.
	if !hasContent && !hasTopic && !hasMetadata {
		return &cliUsageError{"nothing to update; pass --content / --topic / --metadata"}
	}
	metadata, err := cliParseMetadata(metadataRaw)
	if err != nil {
		return err
	}

	m, err2 := cliGetMemory(c)
	if err2 != nil {
		return err2
	}
	defer m.Close()

	var updates []NodeUpdateOption
	if hasContent {
		updates = append(updates, WithNodeContent(content))
	}
	if hasTopic {
		if topicRaw == "" {
			updates = append(updates, WithNodeTopic(nil)) // explicit clear
		} else {
			updates = append(updates, WithNodeTopic(&topicRaw))
		}
	}
	if hasMetadata && metadata != nil {
		updates = append(updates, WithNodeMetadata(metadata))
	}
	updated, err := m.Update(context.Background(), nodeID, updates...)
	if err != nil {
		// Uncaught in Python's update_cmd → treated as a crash; render as
		// ClickException ("Error: ...", exit 1).
		return cliFailf("%v", err)
	}
	if !updated {
		return cliExitNotFound(c, nodeID)
	}

	if c.optBool("output_json") {
		cliOK(c, ord(cliPair{"id", nodeID}, cliPair{"updated", true}))
		return nil
	}
	c.echo(fmt.Sprintf("Updated memory node: %s", nodeID))
	return nil
}

func cliMemoryDelete(c *cliContext, opts map[string]any, args []string) error {
	nodeID := args[0]
	cascade, _ := opts["cascade"].(bool)

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	deleted, err := m.Delete(context.Background(), nodeID, cascade)
	if err != nil {
		// ValueError → ClickException.
		return cliFailf("%v", err)
	}
	if !deleted {
		return cliExitNotFound(c, nodeID)
	}

	if c.optBool("output_json") {
		cliOK(c, ord(cliPair{"id", nodeID}, cliPair{"deleted", true}))
		return nil
	}
	c.echo(fmt.Sprintf("Deleted memory node: %s", nodeID))
	return nil
}

func cliMemoryTree(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	nodes, err := m.GetTree(context.Background())
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(nodes))
		for _, n := range nodes {
			dicts = append(dicts, cliNodeDict(n))
		}
		cliOK(c, ord(cliPair{"nodes", dicts}))
		return nil
	}

	if len(nodes) == 0 {
		c.echo("(empty)")
		return nil
	}

	byID := map[string]*MemoryNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	children := map[string][]*MemoryNode{}
	for _, n := range nodes {
		// Normalize "parent referencing a non-existent node" as orphan.
		parentKey := ""
		if n.ParentID != nil && byID[*n.ParentID] != nil {
			parentKey = *n.ParentID
		}
		children[parentKey] = append(children[parentKey], n)
	}
	// Sort each sibling group by created_at (Python sorts every siblings
	// list); the "" bucket (roots) keeps first-seen node order otherwise.
	for key := range children {
		sorted := children[key]
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.Before(sorted[j].CreatedAt) })
		children[key] = sorted
	}
	roots := children[""]

	var render func(node *MemoryNode, prefix string, isLast bool)
	render = func(node *MemoryNode, prefix string, isLast bool) {
		connector := "|-- "
		if isLast {
			connector = "`-- "
		}
		topicStr := ""
		if node.Topic != nil && *node.Topic != "" {
			topicStr = fmt.Sprintf(" [%s]", *node.Topic)
		}
		c.echo(fmt.Sprintf("%s%s%s%s (%s) <%s>", prefix, connector, node.Content, topicStr, node.Level, node.ID))
		kids := children[node.ID]
		for i, kid := range kids {
			extension := "|   "
			if isLast {
				extension = "    "
			}
			render(kid, prefix+extension, i == len(kids)-1)
		}
	}

	for i, root := range roots {
		topicStr := ""
		if root.Topic != nil && *root.Topic != "" {
			topicStr = fmt.Sprintf(" [%s]", *root.Topic)
		}
		c.echo(fmt.Sprintf("%s%s (%s) <%s>", root.Content, topicStr, root.Level, root.ID))
		kids := children[root.ID]
		for j, kid := range kids {
			render(kid, "", j == len(kids)-1)
		}
		if i != len(roots)-1 {
			c.echo("")
		}
	}
	return nil
}

func cliMemoryRecall(c *cliContext, opts map[string]any, args []string) error {
	query := args[0]
	limit, _ := opts["limit"].(int)

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	nodes, err := m.Recall(context.Background(), query, limit)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(nodes))
		for _, n := range nodes {
			dicts = append(dicts, cliNodeDict(n))
		}
		cliOK(c, ord(cliPair{"nodes", dicts}))
		return nil
	}
	if len(nodes) == 0 {
		c.echo("(no results)")
		return nil
	}
	for _, n := range nodes {
		topicStr := ""
		if n.Topic != nil && *n.Topic != "" {
			topicStr = fmt.Sprintf(" [%s]", *n.Topic)
		}
		c.echo(fmt.Sprintf("%s%s", n.Content, topicStr))
	}
	return nil
}
