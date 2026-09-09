// Package memory stores each session's authoritative memory in memory.db.
// Public functions retain the historical <session>/memory directory argument;
// README files in that directory are migration inputs, never live storage.
package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

const (
	readmeName        = "README.md"
	MaxPathSegmentLen = 64
	MaxBodyChars      = 4000
	RootTargetChars   = 2000
	outlineMaxLen     = 4000
	descriptionMaxLen = 200
	maxOpenDatabases  = 16
	migrationMarker   = "files-to-sqlite-v1"
)

// ErrMigrationRequired means the caller must run Migrate with legacy fallback
// nodes before using this session. It is safe to retry after migration succeeds.
var ErrMigrationRequired = errors.New("memory migration required; retry after Migrate succeeds")

var connections = make(chan struct{}, maxOpenDatabases)
var databaseLocks [64]sync.Mutex

// Node is a hierarchical note. Revision increases on each committed replacement.
// Title and Description preserve legacy metadata; absent descriptions are derived.
type Node struct {
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Body        string `json:"body"`
	Revision    int64  `json:"revision"`
}

// DatabasePath maps the historical memory directory to its sibling database.
func DatabasePath(root string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(root)), "memory.db")
}

// NormalizePath accepts the empty root and normalizes slash-delimited slugs.
func NormalizePath(path string) (string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	segs := make([]string, 0)
	for _, s := range strings.Split(path, "/") {
		if strings.ContainsFunc(s, unicode.IsControl) {
			return "", fmt.Errorf("path segment contains control characters")
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return "", fmt.Errorf("path segment %q is not allowed", s)
		}
		if !utf8.ValidString(s) {
			return "", fmt.Errorf("path is not valid UTF-8")
		}
		if utf8.RuneCountInString(s) > MaxPathSegmentLen {
			return "", fmt.Errorf("path segment exceeds %d characters; use a short slug and put content in BODY", MaxPathSegmentLen)
		}
		segs = append(segs, s)
	}
	return strings.Join(segs, "/"), nil
}

func deriveDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > descriptionMaxLen {
			line = string(runes[:descriptionMaxLen]) + "…"
		}
		return line
	}
	return ""
}

type database struct {
	*sql.DB
	lock *sync.Mutex
}

func (db *database) close() {
	db.DB.Close()
	<-connections
	db.lock.Unlock()
}

func open(root string, migrating bool) (_ *database, err error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("memory directory is empty")
	}
	path, err := filepath.Abs(DatabasePath(root))
	if err != nil {
		return nil, err
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(path))
	lock := &databaseLocks[hash.Sum32()%uint32(len(databaseLocks))]
	// Fixed stripes bound bookkeeping without retaining one connection/mutex
	// per session. Serialize journal/schema initialization and operations for
	// a database: SQLite's busy timeout does not cover every WAL setup race.
	lock.Lock()
	connections <- struct{}{}
	var db *sql.DB
	defer func() {
		if err != nil {
			if db != nil {
				db.Close()
			}
			<-connections
			lock.Unlock()
		}
	}()
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create memory database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open memory database: %w", err)
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	u.RawQuery = "_busy_timeout=5000&_journal_mode=WAL&_synchronous=FULL&_txlock=immediate"
	db, err = sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS memory_nodes (
			path TEXT PRIMARY KEY COLLATE BINARY,
			title TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 1
		);
		CREATE TABLE IF NOT EXISTS memory_metadata (
			key TEXT PRIMARY KEY, value TEXT NOT NULL
		);`)
	if err != nil {
		return nil, fmt.Errorf("initialize memory database: %w", err)
	}
	if !migrating {
		var completed int
		err = db.QueryRow("SELECT count(*) FROM memory_metadata WHERE key = ?", migrationMarker).Scan(&completed)
		if err != nil {
			return nil, err
		}
		if completed == 0 {
			entries, readErr := os.ReadDir(root)
			if readErr != nil && !os.IsNotExist(readErr) {
				return nil, fmt.Errorf("inspect memory migration source: %w", readErr)
			}
			if len(entries) > 0 {
				return nil, ErrMigrationRequired
			}
		}
	}
	return &database{DB: db, lock: lock}, nil
}

func scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	if err := row.Scan(&n.Path, &n.Title, &n.Description, &n.Body, &n.Revision); err != nil {
		return nil, err
	}
	if n.Description == "" {
		n.Description = deriveDescription(n.Body)
	}
	return n, nil
}

const nodeColumns = "path, title, description, body, revision"

// Get returns a complete node, including grandfathered oversized imports.
func Get(root, path string) (*Node, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	db, err := open(root, false)
	if err != nil {
		return nil, err
	}
	defer db.close()
	n, err := scanNode(db.QueryRow("SELECT "+nodeColumns+" FROM memory_nodes WHERE path = ?", norm))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func queryNodes(root, where string, args ...any) ([]*Node, error) {
	db, err := open(root, false)
	if err != nil {
		return nil, err
	}
	defer db.close()
	rows, err := db.Query("SELECT "+nodeColumns+" FROM memory_nodes "+where+" ORDER BY path COLLATE BINARY", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// List returns the whole tree, root first, followed by path-sorted descendants.
func List(root string) ([]*Node, error) {
	return queryNodes(root, "")
}

// Children returns immediate children only, with case-sensitive path matching.
func Children(root, parent string) ([]*Node, error) {
	norm, err := NormalizePath(parent)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if norm != "" {
		prefix = norm + "/"
	}
	return queryNodes(root, `WHERE path != '' AND substr(path, 1, length(?)) = ?
		AND instr(substr(path, length(?) + 1), '/') = 0`, prefix, prefix, prefix)
}

// Set atomically replaces a node and creates any missing ancestors.
func Set(root, path, body string) (string, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	if err := SetBatch(root, []*Node{{Path: norm, Body: body}}); err != nil {
		return "", err
	}
	return norm, nil
}

func normalizeNodes(nodes []*Node, enforceLimit bool) ([]*Node, error) {
	out := make([]*Node, 0, len(nodes))
	seen := make(map[string]bool)
	for _, node := range nodes {
		if node == nil {
			return nil, errors.New("memory batch contains a nil node")
		}
		n := *node
		var err error
		n.Path, err = NormalizePath(n.Path)
		if err != nil {
			return nil, err
		}
		if seen[n.Path] {
			return nil, fmt.Errorf("memory batch contains duplicate path %q", n.Path)
		}
		seen[n.Path] = true
		if enforceLimit && (!utf8.ValidString(n.Body) || utf8.RuneCountInString(n.Body) > MaxBodyChars) {
			return nil, fmt.Errorf("memory node %q body must be valid UTF-8 and at most %d Unicode characters (root target %d)", n.Path, MaxBodyChars, RootTargetChars)
		}
		out = append(out, &n)
	}
	return out, nil
}

// SetBatch commits all replacements and auto-created ancestors together, or none.
// New/updated bodies are limited to MaxBodyChars Unicode code points. The root
// target of RootTargetChars is advisory; migration and CopyTree preserve old bodies.
// Duplicate normalized paths are rejected. Supplied Revision values are ignored.
func SetBatch(root string, nodes []*Node) error {
	normalized, err := normalizeNodes(nodes, true)
	if err != nil {
		return err
	}
	return setNodes(root, normalized)
}

func writeNodes(tx *sql.Tx, nodes []*Node) error {
	for _, n := range nodes {
		var ancestors []string
		if n.Path != "" {
			ancestors = append(ancestors, "")
		}
		for p := n.Path; strings.Contains(p, "/"); {
			p = p[:strings.LastIndex(p, "/")]
			ancestors = append(ancestors, p)
		}
		for _, p := range ancestors {
			if _, err := tx.Exec("INSERT OR IGNORE INTO memory_nodes(path) VALUES (?)", p); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO memory_nodes(path, title, description, body) VALUES (?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET title = excluded.title,
			description = excluded.description, body = excluded.body, revision = memory_nodes.revision + 1`,
			n.Path, n.Title, n.Description, n.Body)
		if err != nil {
			return err
		}
	}
	return nil
}

func setNodes(root string, nodes []*Node) error {
	if len(nodes) == 0 {
		return nil
	}
	db, err := open(root, false)
	if err != nil {
		return err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeNodes(tx, nodes); err != nil {
		return fmt.Errorf("write memory batch: %w", err)
	}
	return tx.Commit()
}

// Delete refuses root deletion and, unless recursive, deletion with descendants.
// All subtree checks and removals occur in a single write transaction.
func Delete(root, path string, recursive bool) (int, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return 0, err
	}
	if norm == "" {
		return 0, errors.New("cannot delete the root memory node")
	}
	db, err := open(root, false)
	if err != nil {
		return 0, err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	const match = "path = ? OR substr(path, 1, length(?)) = ?"
	prefix := norm + "/"
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM memory_nodes WHERE "+match, norm, prefix, prefix).Scan(&count); err != nil {
		return 0, err
	}
	if count > 1 && !recursive {
		return 0, fmt.Errorf("node %q has child nodes; pass --recursive to delete the subtree", norm)
	}
	if _, err := tx.Exec("DELETE FROM memory_nodes WHERE "+match, norm, prefix, prefix); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// Search performs a Unicode case-insensitive substring search, path hits first.
func Search(root, query string) ([]*Node, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is empty")
	}
	all, err := List(root)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	type scored struct {
		node  *Node
		score int
	}
	var hits []scored
	for _, n := range all {
		score := 0
		if strings.Contains(strings.ToLower(n.Path), q) {
			score += 3
		}
		if strings.Contains(strings.ToLower(n.Body), q) ||
			strings.Contains(strings.ToLower(n.Title), q) ||
			strings.Contains(strings.ToLower(n.Description), q) {
			score++
		}
		if score > 0 {
			hits = append(hits, scored{n, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]*Node, len(hits))
	for i, hit := range hits {
		out[i] = hit.node
	}
	return out, nil
}

// Outline returns a body-less, bounded root-first hierarchy for prompts.
func Outline(root string) (string, error) {
	nodes, err := List(root)
	if err != nil {
		return "", err
	}
	const suffix = "…(outline truncated; use memory tools to browse)"
	var lines []string
	length := 0
	for _, n := range nodes {
		if n.Path == "" && strings.TrimSpace(n.Body) == "" && n.Title == "" && n.Description == "" {
			continue
		}
		depth, label := strings.Count(n.Path, "/")+1, n.Path
		if n.Path == "" {
			depth, label = 0, "(root)"
		}
		line := strings.Repeat("  ", depth) + label
		if n.Description != "" {
			line += " — " + n.Description
		}
		if length+utf8.RuneCountInString(line)+1 > outlineMaxLen-utf8.RuneCountInString(suffix)-1 {
			lines = append(lines, suffix)
			break
		}
		lines = append(lines, line)
		length += utf8.RuneCountInString(line) + 1
	}
	return strings.Join(lines, "\n"), nil
}

// Count returns the number of nodes. Use CountWithError for operational decisions.
func Count(root string) int {
	count, _ := CountWithError(root)
	return count
}

// CountWithError reports database and pending-migration errors rather than hiding them.
func CountWithError(root string) (int, error) {
	db, err := open(root, false)
	if err != nil {
		return 0, err
	}
	defer db.close()
	var count int
	err = db.QueryRow("SELECT count(*) FROM memory_nodes").Scan(&count)
	return count, err
}

// IsEmpty is conservative on errors to prevent destructive "empty" fallbacks.
func IsEmpty(root string) bool {
	empty, err := IsEmptyWithError(root)
	return err == nil && empty
}

// IsEmptyWithError reports whether no note contains content or metadata.
func IsEmptyWithError(root string) (bool, error) {
	db, err := open(root, false)
	if err != nil {
		return false, err
	}
	defer db.close()
	var count int
	err = db.QueryRow("SELECT count(*) FROM memory_nodes WHERE body != '' OR title != '' OR description != ''").Scan(&count)
	return count == 0, err
}

// CopyTree atomically merges an authoritative snapshot into dst, preserving
// oversized imported bodies. It never copies or consults backup README files.
func CopyTree(src, dst string) error {
	nodes, err := List(src)
	if err != nil {
		return err
	}
	return setNodes(dst, nodes)
}
