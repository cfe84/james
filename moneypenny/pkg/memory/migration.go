package memory

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Migrate imports the retired memory directory and fallback legacy database
// nodes exactly once. Every directory becomes a node; an existing README.md
// makes the file tree authoritative, even when the file is empty. Once files
// exist, stale legacy rows are not resurrected for deleted paths. README bodies,
// including oversized root notes, are preserved without truncation.
//
// Source files are untouched backups. The completion marker commits in the same
// transaction as all imported nodes. Failures roll back and are explicitly
// returned for retry; call again after fixing the source/IO problem. Call this
// for every session at boot, before exposing tools, supplying all legacy nodes
// and any old flat memory converted by the caller into a fallback node.
func Migrate(root string, fallback []*Node) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("migrate memory %q (not completed; safe to retry): %w", root, err)
		}
	}()
	db, err := open(root, true)
	if err != nil {
		return err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var completed string
	err = tx.QueryRow("SELECT value FROM memory_metadata WHERE key = ?", migrationMarker).Scan(&completed)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	files, err := readMigrationFiles(root)
	if err != nil {
		return err
	}
	for _, node := range files {
		if node.hasReadme {
			fallback = nil
			break
		}
	}
	nodes, err := normalizeNodes(fallback, false)
	if err != nil {
		return err
	}
	merged := make(map[string]*Node, len(nodes))
	for _, node := range nodes {
		merged[node.Path] = node
	}
	for _, node := range files {
		if _, exists := merged[node.Path]; !exists || node.hasReadme {
			merged[node.Path] = node.Node
		}
	}
	nodes = nodes[:0]
	for _, node := range merged {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	if err := writeNodes(tx, nodes); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO memory_metadata(key, value) VALUES (?, datetime('now'))", migrationMarker); err != nil {
		return err
	}
	return tx.Commit()
}

type migrationNode struct {
	*Node
	hasReadme bool
}

func readMigrationFiles(root string) ([]migrationNode, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory migration source is not a directory: %s", root)
	}
	var nodes []migrationNode
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory migration refuses symlink: %s", path)
		}
		if !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			rel = ""
		}
		rel = filepath.ToSlash(rel)
		norm, transformed, err := migrationPath(rel)
		if err != nil {
			return fmt.Errorf("invalid migration directory %q: %w", rel, err)
		}
		n := migrationNode{Node: &Node{Path: norm}}
		if transformed {
			n.Description = "Migrated from a legacy directory name; the original path is retained in the memory backup files."
		}
		readme := filepath.Join(path, readmeName)
		stat, err := os.Lstat(readme)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if !stat.Mode().IsRegular() {
				return fmt.Errorf("memory README is not a regular file: %s", readme)
			}
			body, err := os.ReadFile(readme)
			if err != nil {
				return fmt.Errorf("read migration note %q: %w", rel, err)
			}
			n.Body = string(body)
			n.hasReadme = true
		}
		nodes = append(nodes, n)
		return nil
	})
	return nodes, err
}

// migrationPath preserves canonical legacy paths and maps path components that
// predate the current slug constraints to stable, valid names. The source
// directory remains untouched as a backup, so an import must not make a
// daemon unbootable merely because an agent once used prose as a folder name.
func migrationPath(path string) (string, bool, error) {
	if normalized, err := NormalizePath(path); err == nil && normalized == path {
		return normalized, false, nil
	}
	if path == "" {
		return "", false, nil
	}
	parts := strings.Split(path, "/")
	safe := make([]string, 0, len(parts))
	transformed := false
	for _, part := range parts {
		normalized, err := NormalizePath(part)
		if err == nil && normalized == part {
			safe = append(safe, normalized)
			continue
		}
		sum := sha256.Sum256([]byte(part))
		safe = append(safe, fmt.Sprintf("legacy-%x", sum[:12]))
		transformed = true
	}
	normalized, err := NormalizePath(strings.Join(safe, "/"))
	if err != nil {
		return "", false, err
	}
	return normalized, transformed, nil
}
