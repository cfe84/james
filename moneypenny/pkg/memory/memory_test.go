package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func memoryRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "memory")
}

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
	root := memoryRoot(t)
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
	// Missing ancestors are auto-created as empty database nodes.
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
	root := memoryRoot(t)
	_, _ = Set(root, "", "root")
	if _, err := Delete(root, "", true); err == nil {
		t.Errorf("expected root delete to be refused")
	}
}

func TestListAndChildren(t *testing.T) {
	root := memoryRoot(t)
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
	root := memoryRoot(t)
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
	root := memoryRoot(t)
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
	root := memoryRoot(t)
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
	src := memoryRoot(t)
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

func mustSet(t *testing.T, root, path, body string) {
	t.Helper()
	if _, err := Set(root, path, body); err != nil {
		t.Fatal(err)
	}
}

func mustGet(t *testing.T, root, path string) *Node {
	t.Helper()
	n, err := Get(root, path)
	if err != nil || n == nil {
		t.Fatalf("Get(%q): %+v, %v", path, n, err)
	}
	return n
}

func TestDatabaseAuthoritativeAndOnDemand(t *testing.T) {
	root := memoryRoot(t)
	mustSet(t, root, "topic", "database body")
	if DatabasePath(root) != filepath.Join(filepath.Dir(root), "memory.db") {
		t.Fatal(DatabasePath(root))
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Set must not create the retired folder: %v", err)
	}
	if len(connections) != 0 {
		t.Fatal("database connections leaked")
	}
	n := mustGet(t, root, "topic")
	if n.Revision != 1 {
		t.Fatalf("initial revision = %d", n.Revision)
	}
	mustSet(t, root, "topic", "replacement")
	n = mustGet(t, root, "topic")
	if n.Body != "replacement" || n.Revision != 2 {
		t.Fatalf("replacement: %+v", n)
	}
	if len(connections) != 0 {
		t.Fatal("read connection leaked")
	}
}

func TestUnicodeLimitsAndAtomicValidation(t *testing.T) {
	root := memoryRoot(t)
	body := strings.Repeat("界", MaxBodyChars)
	mustSet(t, root, "", body)
	if n := mustGet(t, root, ""); n.Body != body || n.Revision != 1 {
		t.Fatal("4000-character root must be accepted intact with revision 1")
	}
	for _, bad := range []string{body + "x", string([]byte{0xff})} {
		err := SetBatch(root, []*Node{{Path: "valid", Body: "ok"}, {Path: "invalid", Body: bad}})
		if err == nil {
			t.Fatal("expected body validation error")
		}
		if n, _ := Get(root, "valid"); n != nil {
			t.Fatal("partially wrote invalid batch")
		}
	}
	n := mustGet(t, root, "")
	if n.Body != body || n.Revision != 1 {
		t.Fatal("failed write changed root")
	}
	for _, nodes := range [][]*Node{
		{{Path: "a"}, {Path: "/a/"}},
		{{Path: "a"}, nil},
		{{Path: "a"}, {Path: "../bad"}},
	} {
		if err := SetBatch(root, nodes); err == nil {
			t.Fatal("expected invalid batch")
		}
	}
	if Count(root) != 1 {
		t.Fatal("invalid batches created nodes")
	}
}

func TestBatchRootFirstAndAncestorPreservation(t *testing.T) {
	root := memoryRoot(t)
	mustSet(t, root, "", "root")
	mustSet(t, root, "a", "ancestor")
	err := SetBatch(root, []*Node{{Path: "z/y/x", Body: "deep"}, {Path: "a/b", Body: "child"}})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, n := range nodes {
		paths = append(paths, n.Path)
	}
	if !reflect.DeepEqual(paths, []string{"", "a", "a/b", "z", "z/y", "z/y/x"}) {
		t.Fatal(paths)
	}
	if mustGet(t, root, "").Body != "root" || mustGet(t, root, "a").Body != "ancestor" {
		t.Fatal("ancestor overwritten")
	}
	children, err := Children(root, "")
	if err != nil || len(children) != 2 || children[0].Path != "a" || children[1].Path != "z" {
		t.Fatalf("root children: %+v, %v", children, err)
	}
}

func TestBatchRollbackOnSQLFailure(t *testing.T) {
	root := memoryRoot(t)
	mustSet(t, root, "", "old root")
	db, err := sql.Open("sqlite3", DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_note BEFORE INSERT ON memory_nodes
			WHEN NEW.path = 'fail' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := SetBatch(root, []*Node{{Path: "", Body: "new root"}, {Path: "good/deep", Body: "good"}, {Path: "fail", Body: "bad"}}); err == nil {
		t.Fatal("expected SQL failure")
	}
	if n := mustGet(t, root, ""); n.Body != "old root" || n.Revision != 1 {
		t.Fatalf("root replacement was not rolled back: %+v", n)
	}
	if Count(root) != 1 {
		t.Fatal("nodes or ancestors not rolled back")
	}
}

func TestDeletionLiteralCaseSensitivePaths(t *testing.T) {
	root := memoryRoot(t)
	for _, p := range []string{"a/x", "A/x", "ab/x", "a_/x", "a%/x"} {
		mustSet(t, root, p, p)
	}
	for _, p := range []string{"a", "a_", "a%"} {
		deleted, err := Delete(root, p, true)
		if err != nil || deleted != 2 {
			t.Fatalf("delete %q: %d, %v", p, deleted, err)
		}
	}
	mustGet(t, root, "A/x")
	mustGet(t, root, "ab/x")
	if count, err := Delete(root, "missing", false); err != nil || count != 0 {
		t.Fatalf("delete missing: %d, %v", count, err)
	}
}

func TestOutlineUnicodeBoundAndSearchRanking(t *testing.T) {
	root := memoryRoot(t)
	mustSet(t, root, "", "# Root")
	mustSet(t, root, "ÉTUDE", "path hit")
	mustSet(t, root, "body", "étude")
	results, err := Search(root, "étude")
	if err != nil || len(results) != 2 || results[0].Path != "ÉTUDE" {
		t.Fatalf("unicode ranking: %+v, %v", results, err)
	}
	nodes := make([]*Node, 50)
	for i := range nodes {
		nodes[i] = &Node{Path: strings.Repeat("a", i+1), Body: strings.Repeat("根", 300)}
	}
	if err := SetBatch(root, nodes); err != nil {
		t.Fatal(err)
	}
	outline, err := Outline(root)
	if err != nil || !utf8.ValidString(outline) || utf8.RuneCountInString(outline) > outlineMaxLen {
		t.Fatalf("invalid/big outline %d: %v", utf8.RuneCountInString(outline), err)
	}
	if !strings.HasPrefix(outline, "(root)") || !strings.Contains(outline, "truncated") {
		t.Fatal("outline missing root or truncation indication")
	}
}

func TestConcurrentUpdates(t *testing.T) {
	root := memoryRoot(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Set(root, "", "updated")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := mustGet(t, root, ""); n.Revision != 20 {
		t.Fatalf("lost revisions: %+v", n)
	}
	if len(connections) != 0 {
		t.Fatal("connections leaked")
	}
}

func TestOperationalErrorsNotEmpty(t *testing.T) {
	root := memoryRoot(t)
	if err := os.WriteFile(DatabasePath(root), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(root); err == nil {
		t.Fatal("database error hidden")
	}
	if _, err := CountWithError(root); err == nil {
		t.Fatal("count error hidden")
	}
	if _, err := IsEmptyWithError(root); err == nil || IsEmpty(root) {
		t.Fatal("database errors interpreted as empty memory")
	}
	if len(connections) != 0 {
		t.Fatal("connection leaked on open failure")
	}
}
