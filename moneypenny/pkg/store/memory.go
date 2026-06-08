package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Memory size limits. Bodies and descriptions are capped to force agents to
// hierarchize: detail goes in child nodes, summaries in parents.
const (
	MemoryMaxBodyLen        = 2000
	MemoryMaxDescriptionLen = 200
	// memoryOutlineMaxLen bounds the body-less outline injected into the system
	// prompt each run, so a large tree can't blow up the prompt.
	memoryOutlineMaxLen = 4000
)

// MemoryNode is a single hierarchical memory entry. Hierarchy is expressed
// purely through the slash-delimited materialized path (e.g. "project/git").
type MemoryNode struct {
	Path        string    `json:"path"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// NormalizeMemoryPath cleans a user/agent-supplied memory path: trims spaces and
// slashes from each segment, drops empty segments, lowercases nothing (paths are
// case-sensitive slugs). Returns an error if the result is empty or a segment is
// invalid.
func NormalizeMemoryPath(path string) (string, error) {
	raw := strings.Split(path, "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Forbid control chars and the SQL LIKE wildcards %/_ so paths can never
		// act as wildcard patterns or break prefix matching.
		if strings.ContainsAny(s, "\n\t%_\\") {
			return "", fmt.Errorf("path segment %q contains invalid characters", s)
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return "", fmt.Errorf("path is empty")
	}
	return strings.Join(segs, "/"), nil
}

// ancestorPaths returns all proper ancestor paths of a normalized path, from the
// shallowest to the deepest (e.g. "a/b/c" -> ["a", "a/b"]).
func ancestorPaths(path string) []string {
	segs := strings.Split(path, "/")
	if len(segs) <= 1 {
		return nil
	}
	out := make([]string, 0, len(segs)-1)
	for i := 1; i < len(segs); i++ {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return out
}

// GetMemoryNode returns the node at path, or nil if it does not exist.
func (s *Store) GetMemoryNode(sessionID, path string) (*MemoryNode, error) {
	row := s.db.QueryRow(
		`SELECT path, title, description, body, created_at, updated_at
		 FROM memory_nodes WHERE session_id = ? AND path = ?`, sessionID, path,
	)
	n := &MemoryNode{}
	err := row.Scan(&n.Path, &n.Title, &n.Description, &n.Body, &n.CreatedAt, &n.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get memory node: %w", err)
	}
	return n, nil
}

// ListMemoryNodes returns all nodes for a session ordered by path.
func (s *Store) ListMemoryNodes(sessionID string) ([]*MemoryNode, error) {
	rows, err := s.db.Query(
		`SELECT path, title, description, body, created_at, updated_at
		 FROM memory_nodes WHERE session_id = ? ORDER BY path`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list memory nodes: %w", err)
	}
	defer rows.Close()
	var out []*MemoryNode
	for rows.Next() {
		n := &MemoryNode{}
		if err := rows.Scan(&n.Path, &n.Title, &n.Description, &n.Body, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListMemoryChildren returns the immediate children of parent (or top-level
// nodes when parent is ""). Bodies are included.
func (s *Store) ListMemoryChildren(sessionID, parent string) ([]*MemoryNode, error) {
	all, err := s.ListMemoryNodes(sessionID)
	if err != nil {
		return nil, err
	}
	var out []*MemoryNode
	for _, n := range all {
		if memoryParentOf(n.Path) == parent {
			out = append(out, n)
		}
	}
	return out, nil
}

// memoryParentOf returns the parent path of a node path ("a/b/c" -> "a/b",
// "a" -> "").
func memoryParentOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

// SetMemoryNode creates or replaces a node at path. Missing ancestor nodes are
// auto-created as empty placeholders. Enforces the body/description size caps;
// an over-cap write returns an error instructing the caller to hierarchize.
func (s *Store) SetMemoryNode(sessionID, path, title, description, body string) error {
	path, err := NormalizeMemoryPath(path)
	if err != nil {
		return err
	}
	if len(body) > MemoryMaxBodyLen {
		return fmt.Errorf("node body is %d chars (max %d). Keep detail in child nodes (e.g. %q) and synthesize a summary here",
			len(body), MemoryMaxBodyLen, path+"/<subtopic>")
	}
	if len(description) > MemoryMaxDescriptionLen {
		return fmt.Errorf("node description is %d chars (max %d); keep it to a one-line summary", len(description), MemoryMaxDescriptionLen)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set memory node: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	// Auto-create missing ancestors as empty placeholders.
	for _, anc := range ancestorPaths(path) {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO memory_nodes (session_id, path, created_at, updated_at)
			 VALUES (?, ?, ?, ?)`, sessionID, anc, now, now,
		); err != nil {
			return fmt.Errorf("create ancestor %q: %w", anc, err)
		}
	}

	// Upsert the target node, preserving created_at on replace.
	_, err = tx.Exec(
		`INSERT INTO memory_nodes (session_id, path, title, description, body, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, path) DO UPDATE SET
		   title = excluded.title,
		   description = excluded.description,
		   body = excluded.body,
		   updated_at = excluded.updated_at`,
		sessionID, path, title, description, body, now, now,
	)
	if err != nil {
		return fmt.Errorf("upsert memory node: %w", err)
	}
	return tx.Commit()
}

// DeleteMemoryNode removes the node at path. When recursive is true, all
// descendants are removed too. Returns the number of nodes deleted.
func (s *Store) DeleteMemoryNode(sessionID, path string, recursive bool) (int, error) {
	path, err := NormalizeMemoryPath(path)
	if err != nil {
		return 0, err
	}
	// Use an exact, case-sensitive prefix match (substr) rather than LIKE: SQLite
	// LIKE is case-insensitive for ASCII and treats %/_ as wildcards, either of
	// which could delete unrelated nodes.
	prefix := path + "/"
	plen := len(prefix)
	if recursive {
		res, err := s.db.Exec(
			`DELETE FROM memory_nodes WHERE session_id = ? AND (path = ? OR substr(path, 1, ?) = ?)`,
			sessionID, path, plen, prefix,
		)
		if err != nil {
			return 0, fmt.Errorf("delete memory node: %w", err)
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}
	// Non-recursive: refuse if children exist.
	var childCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM memory_nodes WHERE session_id = ? AND substr(path, 1, ?) = ?`,
		sessionID, plen, prefix,
	).Scan(&childCount); err != nil {
		return 0, fmt.Errorf("count children: %w", err)
	}
	if childCount > 0 {
		return 0, fmt.Errorf("node %q has %d child node(s); pass --recursive to delete the subtree", path, childCount)
	}
	res, err := s.db.Exec(
		`DELETE FROM memory_nodes WHERE session_id = ? AND path = ?`, sessionID, path,
	)
	if err != nil {
		return 0, fmt.Errorf("delete memory node: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// SearchMemoryNodes returns nodes matching query (case-insensitive substring
// over path/title/description/body), ranked so that matches in path/title/
// description sort before body-only matches.
func (s *Store) SearchMemoryNodes(sessionID, query string) ([]*MemoryNode, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	all, err := s.ListMemoryNodes(sessionID)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	type scored struct {
		node  *MemoryNode
		score int
	}
	var hits []scored
	for _, n := range all {
		score := 0
		if strings.Contains(strings.ToLower(n.Path), q) {
			score += 4
		}
		if strings.Contains(strings.ToLower(n.Title), q) {
			score += 3
		}
		if strings.Contains(strings.ToLower(n.Description), q) {
			score += 2
		}
		if strings.Contains(strings.ToLower(n.Body), q) {
			score++
		}
		if score > 0 {
			hits = append(hits, scored{node: n, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].node.Path < hits[j].node.Path
	})
	out := make([]*MemoryNode, len(hits))
	for i, h := range hits {
		out[i] = h.node
	}
	return out, nil
}

// MemoryOutline returns a compact, body-less outline of the memory tree (path +
// description, indented by depth), suitable for injecting into the system
// prompt. Returns "" when there are no nodes. Output is capped to
// memoryOutlineMaxLen; deeper entries are dropped first if needed.
func (s *Store) MemoryOutline(sessionID string) (string, error) {
	nodes, err := s.ListMemoryNodes(sessionID)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", nil
	}
	// nodes are already ordered by path, which yields a correct DFS pre-order
	// for a materialized-path tree.
	var b strings.Builder
	for _, n := range nodes {
		depth := strings.Count(n.Path, "/")
		line := strings.Repeat("  ", depth) + n.Path
		label := n.Title
		if label == "" {
			label = n.Description
		} else if n.Description != "" {
			label = n.Title + " — " + n.Description
		}
		if label != "" {
			line += " — " + label
		}
		line += "\n"
		if b.Len() > 0 && b.Len()+len(line) > memoryOutlineMaxLen {
			b.WriteString("…(outline truncated; use 'show memory' to browse)\n")
			break
		}
		b.WriteString(line)
		if b.Len() > memoryOutlineMaxLen {
			b.WriteString("…(outline truncated; use 'show memory' to browse)\n")
			break
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// MemoryNodeCount returns the number of memory nodes for a session.
func (s *Store) MemoryNodeCount(sessionID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_nodes WHERE session_id = ?`, sessionID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count memory nodes: %w", err)
	}
	return n, nil
}

// MigrateLegacyMemory imports a session's legacy blob memory into a single node
// at path "notes" when nodes don't yet exist. Idempotent: it no-ops if any
// memory node already exists or the blob is empty. Returns true if it imported.
func (s *Store) MigrateLegacyMemory(sessionID string) (bool, error) {
	count, err := s.MemoryNodeCount(sessionID)
	if err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil
	}
	blob, err := s.GetMemory(sessionID)
	if err != nil {
		return false, err
	}
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return false, nil
	}
	// Import as one node. Bypass the body cap for the legacy import so we never
	// lose existing content; the agent can split it later. The INSERT ... SELECT
	// ... WHERE NOT EXISTS makes the "no nodes yet" check and the insert atomic,
	// so concurrent first accesses can't double-import or hit a constraint error.
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO memory_nodes (session_id, path, title, description, body, created_at, updated_at)
		 SELECT ?, 'notes', 'Imported notes', 'Legacy memory imported from the previous flat note', ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM memory_nodes WHERE session_id = ?)`,
		sessionID, blob, now, now, sessionID,
	)
	if err != nil {
		return false, fmt.Errorf("import legacy memory: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
