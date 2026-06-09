// Package memory implements James's per-session memory as a plain folder of
// Markdown files on the moneypenny host, rather than rows in SQLite. Every node
// is a directory containing a README.md: the root note lives at
// <root>/README.md, and a node at path "a/b" lives at <root>/a/b/README.md.
//
// This lets agents read and edit their own memory with their native file tools
// (no shell-out to hem), which is both simpler and more reliable — the previous
// design exposed memory only via hem shell commands, which agents sometimes
// could not find in their tool list and worked around with ad-hoc notes files.
//
// All functions take an absolute root directory and operate purely on the
// filesystem. Paths supplied by agents/users are normalized and validated to
// prevent traversal outside root.
package memory

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// readmeName is the per-folder note/index file.
	readmeName = "README.md"
	// MaxPathSegmentLen bounds a single path segment. Paths are short slugs
	// (e.g. "project/conventions/git"), not prose; a long segment almost always
	// means content was passed as the PATH instead of the BODY.
	MaxPathSegmentLen = 64
	// outlineMaxLen bounds the body-less outline injected into the system prompt
	// so a large tree can't blow up the prompt.
	outlineMaxLen = 4000
	// descriptionMaxLen bounds the derived one-line description for display.
	descriptionMaxLen = 200
	// maxBodyReadBytes bounds how much of a README is read into memory for bulk
	// operations (List/Children/Search/outline). Agents now edit memory files
	// directly, so a pathologically large note must not be able to exhaust the
	// daemon's memory or produce oversized responses. Normal notes are far
	// smaller than this; truncation only affects degenerate cases.
	maxBodyReadBytes = 1 << 20 // 1 MiB
)

// Node is a single memory node. Title is always empty (the file model has no
// separate title); Description is derived from the body for display.
type Node struct {
	Path        string
	Title       string
	Description string
	Body        string
}

// NormalizePath cleans a user/agent-supplied memory path. It trims spaces and
// slashes from each segment, drops empty segments, and rejects traversal,
// absolute paths, control characters, path separators, and prose-length
// segments. The empty path is valid and refers to the root node.
func NormalizePath(path string) (string, error) {
	// Normalize Windows separators to forward slashes first.
	path = strings.ReplaceAll(path, "\\", "/")
	raw := strings.Split(path, "/")
	segs := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if s == "." || s == ".." {
			return "", fmt.Errorf("path segment %q is not allowed", s)
		}
		if strings.ContainsAny(s, "\n\r\t\x00") {
			return "", fmt.Errorf("path segment contains control characters")
		}
		if len(s) > MaxPathSegmentLen {
			return "", fmt.Errorf("path segment is %d chars (max %d). PATH must be a short slug like \"project/topic\"; put the content in BODY, not the path",
				len(s), MaxPathSegmentLen)
		}
		segs = append(segs, s)
	}
	return strings.Join(segs, "/"), nil
}

// dirFor returns the absolute directory for a normalized path under root.
func dirFor(root, normPath string) string {
	if normPath == "" {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(normPath))
}

// readmeFor returns the absolute README.md path for a normalized path.
func readmeFor(root, normPath string) string {
	return filepath.Join(dirFor(root, normPath), readmeName)
}

// deriveDescription returns a one-line summary derived from a README body: the
// first markdown heading text, else the first non-empty line, trimmed and
// capped.
func deriveDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > descriptionMaxLen {
			line = line[:descriptionMaxLen] + "…"
		}
		return line
	}
	return ""
}

// readBody reads the README body for a normalized path, returning "" if absent.
// The read is bounded by maxBodyReadBytes so a degenerate (huge) note can't
// exhaust memory during bulk operations.
func readBody(root, normPath string) string {
	f, err := os.Open(readmeFor(root, normPath))
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, maxBodyReadBytes)
	n, _ := io.ReadFull(f, buf)
	return string(buf[:n])
}

// Get returns the node at path, or nil if neither the directory nor its README
// exists.
func Get(root, path string) (*Node, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	dir := dirFor(root, norm)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		// Root may not exist yet; treat as absent.
		return nil, nil
	}
	body := readBody(root, norm)
	return &Node{Path: norm, Description: deriveDescription(body), Body: body}, nil
}

// List returns every node in the tree (each directory under root, including the
// root) ordered by path. Bodies are included.
func List(root string) ([]*Node, error) {
	var nodes []*Node
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, nil
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		norm := filepath.ToSlash(rel)
		if norm == "." {
			norm = ""
		}
		body := readBody(root, norm)
		nodes = append(nodes, &Node{Path: norm, Description: deriveDescription(body), Body: body})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list memory: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Path < nodes[j].Path })
	return nodes, nil
}

// Children returns the immediate child nodes of parent (or top-level nodes when
// parent is "").
func Children(root, parent string) ([]*Node, error) {
	norm, err := NormalizePath(parent)
	if err != nil {
		return nil, err
	}
	dir := dirFor(root, norm)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list children: %w", err)
	}
	var out []*Node
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		childPath := e.Name()
		if norm != "" {
			childPath = norm + "/" + e.Name()
		}
		body := readBody(root, childPath)
		out = append(out, &Node{Path: childPath, Description: deriveDescription(body), Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Set creates or replaces the node at path, writing body to its README.md.
// Missing ancestor directories are created automatically.
func Set(root, path, body string) (string, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	dir := dirFor(root, norm)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create memory dir: %w", err)
	}
	// Write atomically (temp file + rename) so a failed or interrupted write
	// never leaves a half-written README behind — important during migration,
	// where a non-empty folder is treated as "already migrated".
	target := filepath.Join(dir, readmeName)
	tmp, err := os.CreateTemp(dir, ".readme-*.tmp")
	if err != nil {
		return "", fmt.Errorf("write memory note: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write([]byte(body)); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write memory note: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write memory note: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write memory note: %w", err)
	}
	return norm, nil
}

// Delete removes the node at path. When recursive is true, the whole subtree is
// removed; otherwise it refuses if the node has child folders. The root node
// (empty path) cannot be deleted. Returns the number of nodes removed.
func Delete(root, path string, recursive bool) (int, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return 0, err
	}
	if norm == "" {
		return 0, fmt.Errorf("cannot delete the root memory node")
	}
	dir := dirFor(root, norm)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return 0, nil
	}
	// Count descendant nodes (directories) for the return value / child check.
	var childDirs int
	descendants := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		descendants++
		if p != dir {
			childDirs++
		}
		return nil
	})
	if !recursive && childDirs > 0 {
		return 0, fmt.Errorf("node %q has %d child node(s); pass --recursive to delete the subtree", norm, childDirs)
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, fmt.Errorf("delete memory node: %w", err)
	}
	return descendants, nil
}

// Search returns nodes whose path or body matches query (case-insensitive
// substring), ranked so path matches sort before body-only matches.
func Search(root, query string) ([]*Node, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is empty")
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
	out := make([]*Node, len(hits))
	for i, h := range hits {
		out[i] = h.node
	}
	return out, nil
}

// Outline returns a compact, body-less tree outline (path + derived
// description, indented by depth), suitable for the system prompt. Returns ""
// when the tree is empty (only the root with no content). Capped to
// outlineMaxLen.
func Outline(root string) (string, error) {
	nodes, err := List(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, n := range nodes {
		if n.Path == "" && strings.TrimSpace(n.Body) == "" {
			continue
		}
		depth := 0
		label := n.Path
		if n.Path == "" {
			label = "(root)"
		} else {
			depth = strings.Count(n.Path, "/") + 1
		}
		line := strings.Repeat("  ", depth) + label
		if d := n.Description; d != "" {
			line += " — " + d
		}
		line += "\n"
		if b.Len() > 0 && b.Len()+len(line) > outlineMaxLen {
			b.WriteString("…(outline truncated; read individual README.md files to browse)\n")
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Count returns the number of nodes (directories) in the tree.
func Count(root string) int {
	nodes, _ := List(root)
	return len(nodes)
}

// IsEmpty reports whether the memory tree has no content: either the root dir is
// absent, or it contains no README files at all.
func IsEmpty(root string) bool {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return true
	}
	empty := true
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == readmeName {
			empty = false
		}
		return nil
	})
	return empty
}

// CopyTree copies the entire memory tree from src to dst (used when duplicating
// a session). It is a no-op when src does not exist.
func CopyTree(src, dst string) error {
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
