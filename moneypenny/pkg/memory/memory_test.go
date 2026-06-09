package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"/", "", false},
		{"a/b", "a/b", false},
		{" a / b ", "a/b", false},
		{"a//b/", "a/b", false},
		{"a\\b", "a/b", false},
		{"../etc", "", true},
		{"a/../b", "", true},
		{"a/./b", "", true},
		{"a\nb", "", true},
	}
	for _, c := range cases {
		got, err := NormalizePath(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizePath(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizePath(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePathSegmentCap(t *testing.T) {
	long := ""
	for i := 0; i <= MaxPathSegmentLen; i++ {
		long += "x"
	}
	if _, err := NormalizePath(long); err == nil {
		t.Fatalf("expected error for over-cap segment")
	}
}

func TestSetGetDelete(t *testing.T) {
	root := t.TempDir()
	if _, err := Set(root, "project/git", "# Git\nUse semver."); err != nil {
		t.Fatalf("Set: %v", err)
	}
	n, err := Get(root, "project/git")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if n == nil || n.Body != "# Git\nUse semver." {
		t.Fatalf("Get returned %+v", n)
	}
	if n.Description != "Git" {
		t.Errorf("derived description = %q, want %q", n.Description, "Git")
	}
	// Ancestor folder auto-created (but no README yet) -> node exists, empty body.
	parent, err := Get(root, "project")
	if err != nil || parent == nil {
		t.Fatalf("ancestor Get: %v %+v", err, parent)
	}
	if parent.Body != "" {
		t.Errorf("ancestor body = %q, want empty", parent.Body)
	}

	// Non-recursive delete of a node with children must fail.
	if _, err := Delete(root, "project", false); err == nil {
		t.Errorf("expected non-recursive delete to fail on parent with children")
	}
	deleted, err := Delete(root, "project", true)
	if err != nil {
		t.Fatalf("recursive Delete: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if n, _ := Get(root, "project/git"); n != nil {
		t.Errorf("node still present after delete")
	}
}

func TestDeleteRootRefused(t *testing.T) {
	root := t.TempDir()
	_, _ = Set(root, "", "root")
	if _, err := Delete(root, "", true); err == nil {
		t.Errorf("expected root delete to be refused")
	}
}

func TestListAndChildren(t *testing.T) {
	root := t.TempDir()
	_, _ = Set(root, "", "# Root")
	_, _ = Set(root, "a", "# A")
	_, _ = Set(root, "a/b", "# A B")
	_, _ = Set(root, "c", "# C")

	all, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantPaths := map[string]bool{"": true, "a": true, "a/b": true, "c": true}
	if len(all) != len(wantPaths) {
		t.Fatalf("List returned %d nodes, want %d", len(all), len(wantPaths))
	}
	for _, n := range all {
		if !wantPaths[n.Path] {
			t.Errorf("unexpected node path %q", n.Path)
		}
	}

	children, err := Children(root, "a")
	if err != nil {
		t.Fatalf("Children: %v", err)
	}
	if len(children) != 1 || children[0].Path != "a/b" {
		t.Errorf("Children(a) = %+v", children)
	}
}

func TestSearch(t *testing.T) {
	root := t.TempDir()
	_, _ = Set(root, "project/git", "We use semver for versioning.")
	_, _ = Set(root, "other", "Unrelated content.")

	res, err := Search(root, "semver")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Path != "project/git" {
		t.Errorf("Search(semver) = %+v", res)
	}
}

func TestOutlineAndIsEmpty(t *testing.T) {
	root := t.TempDir()
	if !IsEmpty(root) {
		t.Errorf("new root should be empty")
	}
	_, _ = Set(root, "a", "# Topic A\nbody")
	if IsEmpty(root) {
		t.Errorf("root with content should not be empty")
	}
	out, err := Outline(root)
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}
	if out == "" {
		t.Errorf("expected non-empty outline")
	}
}

func TestTraversalCannotEscape(t *testing.T) {
	root := t.TempDir()
	// A traversal attempt must be rejected by NormalizePath, so nothing is
	// written outside root.
	if _, err := Set(root, "../escape", "x"); err == nil {
		t.Fatalf("expected Set with traversal to fail")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatalf("traversal escaped root")
	}
}

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "memory")
	_, _ = Set(src, "", "# Root")
	_, _ = Set(src, "a/b", "# A B")
	if err := CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	n, err := Get(dst, "a/b")
	if err != nil || n == nil || n.Body != "# A B" {
		t.Fatalf("copied node = %+v err=%v", n, err)
	}
}
