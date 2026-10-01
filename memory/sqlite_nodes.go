package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Memory tree CRUD. Mirrors the "Memory tree" section of sqlite.py.

// SaveNode inserts a new memory node.
//
// Strict insert (no INSERT OR REPLACE): re-inserting the same ID returns a
// unique-constraint error. Updates must go through UpdateNode explicitly.
// This is defensive — silent overwrite would let buggy callers lose data
// without any signal.
func (b *SqliteMemoryBackend) SaveNode(ctx context.Context, node *MemoryNode) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if node.Level == NodeLevelLeaf && node.AtomID == nil {
		return errors.New("memory: leaf nodes must reference an AtomCard via atom_id")
	}
	if node.Level != NodeLevelLeaf && node.AtomID != nil {
		return errors.New("memory: root and branch nodes cannot reference an AtomCard")
	}
	// Leaf content is stored as the empty string; reads project the
	// AtomCard assertion back in (see scanNode).
	storedContent := node.Content
	if node.Level == NodeLevelLeaf && node.AtomID != nil {
		storedContent = ""
	}
	meta := node.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_memory_nodes
                (id, parent_id, level, atom_id, content, topic, conversation_id, created_at, updated_at, metadata)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		node.ID, node.ParentID, string(node.Level), node.AtomID, storedContent,
		node.Topic, node.ConversationID, formatTime(node.CreatedAt), formatTime(node.UpdatedAt),
		mustMarshalJSON(meta))
	return err
}

// GetNode fetches a single node by id, or nil if not found.
func (b *SqliteMemoryBackend) GetNode(ctx context.Context, nodeID string) (*MemoryNode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT %[2]s
                FROM %[1]s_memory_nodes n
                LEFT JOIN %[1]s_atoms a ON a.id = n.atom_id
                WHERE n.id = ?`, b.ns, nodeSelectColumns)
	row := b.DB().QueryRowContext(ctx, q, nodeID)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return node, err
}

// GetNodeByAtomID fetches the leaf node that references atomID, or nil.
func (b *SqliteMemoryBackend) GetNodeByAtomID(ctx context.Context, atomID string) (*MemoryNode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT %[2]s
                FROM %[1]s_memory_nodes n
                LEFT JOIN %[1]s_atoms a ON a.id = n.atom_id
                WHERE n.atom_id = ?`, b.ns, nodeSelectColumns)
	row := b.DB().QueryRowContext(ctx, q, atomID)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return node, err
}

// GetChildren returns the direct children of parentID (nil = root level).
func (b *SqliteMemoryBackend) GetChildren(ctx context.Context, parentID *string) ([]*MemoryNode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	base := fmt.Sprintf(`SELECT %[2]s
                FROM %[1]s_memory_nodes n
                LEFT JOIN %[1]s_atoms a ON a.id = n.atom_id`, b.ns, nodeSelectColumns)
	var rows *sql.Rows
	var err error
	if parentID == nil {
		rows, err = b.DB().QueryContext(ctx, base+" WHERE n.parent_id IS NULL")
	} else {
		rows, err = b.DB().QueryContext(ctx, base+" WHERE n.parent_id = ?", *parentID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// UpdateNode updates one or more mutable fields of an existing node.
//
// ParentID and Level are intentionally NOT mutable here: structural moves
// and tree validation are responsibilities of the service layer. Metadata
// semantics is replace (whole-dict overwrite), not patch-merge.
//
// Pass nil for a field to leave it untouched; pass a non-nil pointer (or
// WithTopic("")/WithNodeMetadata(map) values) to set it. Returns true if
// the node existed and was updated, else false.
func (b *SqliteMemoryBackend) UpdateNode(ctx context.Context, nodeID string, opts ...NodeUpdateOption) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	var opt nodeUpdate
	for _, o := range opts {
		o(&opt)
	}
	var sets []string
	var params []any
	if opt.content != nil {
		sets = append(sets, "content = ?")
		params = append(params, *opt.content)
	}
	if opt.topicSet {
		sets = append(sets, "topic = ?")
		if opt.topic == nil {
			params = append(params, nil)
		} else {
			params = append(params, *opt.topic)
		}
	}
	if opt.metadata != nil {
		sets = append(sets, "metadata = ?")
		params = append(params, mustMarshalJSON(opt.metadata))
	}

	if len(sets) == 0 {
		// Nothing to update; still verify existence for honest return value.
		var one int
		err := b.DB().QueryRowContext(ctx,
			fmt.Sprintf("SELECT 1 FROM %s_memory_nodes WHERE id = ?", b.ns), nodeID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}

	sets = append(sets, "updated_at = ?")
	params = append(params, formatTime(time.Now().UTC()), nodeID)

	q := fmt.Sprintf("UPDATE %s_memory_nodes SET %s WHERE id = ?", b.ns, strings.Join(sets, ", "))
	res, err := b.DB().ExecContext(ctx, q, params...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

type nodeUpdate struct {
	content  *string
	topic    *string
	topicSet bool
	metadata map[string]any
}

// NodeUpdateOption customizes UpdateNode.
type NodeUpdateOption func(*nodeUpdate)

// WithNodeContent sets the node content.
func WithNodeContent(content string) NodeUpdateOption { return func(u *nodeUpdate) { u.content = &content } }

// WithNodeTopic sets (or clears with nil) the node topic.
func WithNodeTopic(topic *string) NodeUpdateOption {
	return func(u *nodeUpdate) { u.topic = topic; u.topicSet = true }
}

// WithNodeMetadata replaces the whole metadata dict.
func WithNodeMetadata(metadata map[string]any) NodeUpdateOption {
	return func(u *nodeUpdate) { u.metadata = metadata }
}

// DeleteNode deletes a node by id.
//
// If cascade is false and the node has children, an error is returned. With
// cascade=true the entire subtree rooted at nodeID is deleted recursively.
// The FTS index is kept in sync via the {ns}_memory_ad trigger so deleted
// nodes never linger in search results. Returns true if a row was deleted.
func (b *SqliteMemoryBackend) DeleteNode(ctx context.Context, nodeID string, cascade bool) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	var one int
	err := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT 1 FROM %s_memory_nodes WHERE id = ?", b.ns), nodeID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if cascade {
		// Collect entire subtree (BFS) to delete in one shot.
		toDelete := []string{nodeID}
		frontier := []string{nodeID}
		for len(frontier) > 0 {
			placeholders := placeholders(len(frontier))
			args := make([]any, len(frontier))
			for i, id := range frontier {
				args[i] = id
			}
			rows, err := b.DB().QueryContext(ctx,
				fmt.Sprintf("SELECT id FROM %s_memory_nodes WHERE parent_id IN (%s)", b.ns, placeholders),
				args...)
			if err != nil {
				return false, err
			}
			var childIDs []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return false, err
				}
				childIDs = append(childIDs, id)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return false, err
			}
			toDelete = append(toDelete, childIDs...)
			frontier = childIDs
		}
		args := make([]any, len(toDelete))
		for i, id := range toDelete {
			args[i] = id
		}
		if _, err := b.DB().ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s_memory_nodes WHERE id IN (%s)", b.ns, placeholders(len(toDelete))),
			args...); err != nil {
			return false, err
		}
	} else {
		var childCount int
		if err := b.DB().QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s_memory_nodes WHERE parent_id = ?", b.ns),
			nodeID).Scan(&childCount); err != nil {
			return false, err
		}
		if childCount > 0 {
			return false, fmt.Errorf("memory: node %q has %d child(ren); pass cascade=true to delete subtree", nodeID, childCount)
		}
		if _, err := b.DB().ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s_memory_nodes WHERE id = ?", b.ns), nodeID); err != nil {
			return false, err
		}
	}
	return true, nil
}

// SearchMemories FTS-searches the atom projection of the memory tree and
// returns matching leaf nodes (atoms_fts JOIN atoms JOIN memory_nodes).
func (b *SqliteMemoryBackend) SearchMemories(ctx context.Context, query string, limit int) ([]*MemoryNode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	safeQuery := FTSMatchPhrase(query)
	q := fmt.Sprintf(`SELECT %[2]s
                FROM %[1]s_atoms_fts f
                JOIN %[1]s_atoms a ON f.rowid = a.rowid
                JOIN %[1]s_memory_nodes n ON n.atom_id = a.id
                WHERE f.%[1]s_atoms_fts MATCH ?
                  AND a.deprecated_at IS NULL
                ORDER BY rank
                LIMIT ?`, b.ns, nodeSelectColumns)
	rows, err := b.DB().QueryContext(ctx, q, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// GetTree returns every node in the memory tree.
func (b *SqliteMemoryBackend) GetTree(ctx context.Context) ([]*MemoryNode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`SELECT %[2]s
                FROM %[1]s_memory_nodes n
                LEFT JOIN %[1]s_atoms a ON a.id = n.atom_id`, b.ns, nodeSelectColumns)
	rows, err := b.DB().QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

// placeholders builds "?,?,?" with n marks.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
