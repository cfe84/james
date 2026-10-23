package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func testClaim(path, title string) Claim {
	return Claim{
		Path: path, Title: title, Body: "A bounded observation with evidence.",
		Kind: "fact", Status: "active", Confidence: "supported", Importance: "useful",
		Provenance: []Provenance{{Method: "document", Source: "design.md"}},
	}
}

func TestClaimsEvidenceCorrectionsAndLifecycle(t *testing.T) {
	root := memoryRoot(t)
	if _, err := Set(root, "engineering", "Original topic note"); err != nil {
		t.Fatal(err)
	}
	belief := testClaim("engineering", "Old deployment")
	belief.ReviewAfter = "2020-01-01T00:00:00Z"
	belief.VerifiedAt = "2019-01-01T00:00:00Z"
	old, err := SetClaim(root, belief)
	if err != nil {
		t.Fatal(err)
	}
	if !old.Stale || old.Revision != 1 || old.RecordedAt == "" {
		t.Fatalf("missing metadata: %+v", old)
	}
	correction := testClaim("engineering", "Deployment correction")
	correction.Kind = "observation"
	correction.Links = []Link{{Target: old.ID, Relation: "supersedes"}}
	newer, err := SetClaim(root, correction)
	if err != nil {
		t.Fatal(err)
	}
	old.Status = "disproven"
	old.Body = "Disproven after inspecting the deployed configuration."
	old, err = SetClaim(root, *old)
	if err != nil {
		t.Fatal(err)
	}
	if old.Revision != 2 || len(old.Backlinks) != 1 || old.Backlinks[0].Title != newer.Title {
		t.Fatalf("correction not discoverable: %+v", old)
	}
	all, err := QueryClaims(root, Query{Text: "DEPLOYMENT"})
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("ordinary retrieval hid a correction: %+v %v", all, err)
	}
	stale := true
	hits, err := QueryClaims(root, Query{Path: "engineering", Status: "disproven", Confidence: "supported", Importance: "useful", Method: "document", Source: "design", Stale: &stale, VerifiedBefore: "2021-01-01T00:00:00Z"})
	if err != nil || len(hits.Items) != 1 || hits.Items[0].ID != old.ID {
		t.Fatalf("metadata query: %+v, %v", hits, err)
	}
	hits, err = QueryClaims(root, Query{LinkedTo: old.ID, Relation: "supersedes"})
	if err != nil || len(hits.Items) != 1 || hits.Items[0].ID != newer.ID {
		t.Fatalf("link query: %+v %v", hits, err)
	}
	outdated := *old
	outdated.Revision = 1
	if _, err := SetClaim(root, outdated); err == nil {
		t.Fatal("stale update overwrote correction")
	}
	if _, err := Delete(root, "engineering", true); err == nil {
		t.Fatal("topic deletion erased claims")
	}
	if _, err := Set(root, "engineering/history", "Moved claim topic"); err != nil {
		t.Fatal(err)
	}
	old.Path = "engineering/history"
	moved, err := SetClaim(root, *old)
	if err != nil || moved.ID != old.ID {
		t.Fatalf("move lost stable identity: %+v %v", moved, err)
	}
	dst := memoryRoot(t)
	if err := CopyTree(root, dst); err != nil {
		t.Fatal(err)
	}
	copied, err := GetClaim(dst, old.ID)
	if err != nil || copied.Status != "disproven" || copied.Revision != moved.Revision || len(copied.Backlinks) != 1 || copied.RecordedAt != old.RecordedAt {
		t.Fatalf("copy lost structured memory: %+v %v", copied, err)
	}
	if _, err := Set(dst, "engineering", "Destination sentinel"); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(root, dst); err == nil {
		t.Fatal("copy overwrote an existing claim ID")
	}
	if note, err := Get(dst, "engineering"); err != nil || note.Body != "Destination sentinel" {
		t.Fatalf("copy conflict partially changed destination: %+v %v", note, err)
	}
	if err := DeleteClaim(root, newer.ID, newer.Revision+1); err == nil {
		t.Fatal("stale deletion succeeded")
	}
	if err := DeleteClaim(root, newer.ID, newer.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := GetClaim(root, old.ID)
	if err != nil || len(after.Backlinks) != 0 {
		t.Fatalf("dangling links after erase: %+v %v", after, err)
	}
	note, err := Get(root, "engineering")
	if err != nil || note.Body != "Original topic note" {
		t.Fatal("claim operations modified legacy notes")
	}
}

func TestMemoryQueryBudgetsAndPaging(t *testing.T) {
	root := memoryRoot(t)
	if _, err := Set(root, "topic", "topic"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		c := testClaim("topic", fmt.Sprintf("Claim %02d", i))
		if _, err := SetClaim(root, c); err != nil {
			t.Fatal(err)
		}
	}
	page, err := QueryClaims(root, Query{})
	if err != nil || len(page.Items) != DefaultResults || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("default page: %+v %v", page, err)
	}
	if _, err := QueryClaims(root, Query{Text: "changed", Cursor: page.NextCursor}); err == nil {
		t.Fatal("cursor accepted different query")
	}
	seen := map[string]bool{}
	q := Query{Limit: MaxResults}
	for {
		page, err = QueryClaims(root, q)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if utf8.RuneCount(encoded) > MaxResponseCharacters || len(page.Items) > MaxResults {
			t.Fatal("response exceeded budget")
		}
		for _, c := range page.Items {
			if seen[c.ID] {
				t.Fatal("pagination repeated a claim")
			}
			seen[c.ID] = true
		}
		if !page.HasMore {
			break
		}
		if len(page.Items) == 0 || page.NextCursor == q.Cursor {
			t.Fatal("pagination made no progress")
		}
		q.Cursor = page.NextCursor
	}
	if len(seen) != 55 {
		t.Fatalf("lost claims: %d", len(seen))
	}
	for i := 0; i < 4; i++ {
		heavy := testClaim("topic", "Evidence-heavy decision")
		heavy.Kind = "decision"
		heavy.Body = strings.Repeat("x", 500)
		for n := 0; n < 7; n++ {
			heavy.Provenance = append(heavy.Provenance, Provenance{Method: "observed", Source: "log:" + strings.Repeat("\t", 250)})
		}
		if _, err := SetClaim(root, heavy); err != nil {
			t.Fatal(err)
		}
	}
	heavy, err := QueryClaims(root, Query{Kind: "decision", Limit: 50})
	if err != nil || !heavy.HasMore || len(heavy.Items) >= 4 || len(heavy.Items) == 0 {
		t.Fatalf("serialized size limit not applied: %+v %v", heavy, err)
	}
	encodedHeavy, _ := json.Marshal(heavy)
	if utf8.RuneCount(encodedHeavy) > MaxResponseCharacters {
		t.Fatal("escaped evidence exceeded query budget")
	}
	if _, err := SetClaim(root, *heavy.Items[0]); err == nil {
		t.Fatal("summary update truncated the stored claim")
	}
	for _, q := range []Query{{Limit: 51}, {Limit: -1}, {Status: "guessed"}, {Relation: "supports"}, {Cursor: "broken"}} {
		if _, err := QueryClaims(root, q); err == nil {
			t.Fatalf("accepted invalid query: %+v", q)
		}
	}
	// JSON escaping, not just body length, determines the actual page budget.
	if _, err := Set(root, "topic/escaped", strings.Repeat("\x01", 4000)); err != nil {
		t.Fatal(err)
	}
	read, err := ReadBounded(root, "topic/escaped", 0, MaxReadCharacters)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(read)
	if utf8.RuneCount(encoded) > MaxResponseCharacters || read.NextOffset == nil {
		t.Fatalf("unbounded note read: %d", len(encoded))
	}
	all := ""
	for offset := 0; ; {
		part, err := ReadBounded(root, "topic/escaped", offset, MaxReadCharacters)
		if err != nil {
			t.Fatal(err)
		}
		all += part.Body
		if part.NextOffset == nil {
			break
		}
		offset = *part.NextOffset
	}
	if all != strings.Repeat("\x01", 4000) {
		t.Fatal("paging changed stored content")
	}
	if _, err := Set(root, "", "topic Étage"); err != nil {
		t.Fatal(err)
	}
	unicode, err := QueryNotes(root, Query{Text: "étage"}, false)
	if err != nil || len(unicode.Items) != 1 || unicode.Items[0].Path != "" {
		t.Fatalf("Unicode search changed: %+v %v", unicode, err)
	}
	notes, err := QueryNotes(root, Query{Text: "topic", Limit: 1}, false)
	if err != nil || !notes.HasMore || len(notes.Items) != 1 {
		t.Fatalf("note search not paged: %+v %v", notes, err)
	}
	next, err := QueryNotes(root, Query{Text: "topic", Limit: 1, Cursor: notes.NextCursor}, false)
	if err != nil || len(next.Items) != 1 || next.Items[0].Path == notes.Items[0].Path {
		t.Fatalf("note continuation: %+v %v", next, err)
	}
}

func TestClaimLinkedTitlesFitFullResponse(t *testing.T) {
	root := memoryRoot(t)
	if _, err := Set(root, "topic", ""); err != nil {
		t.Fatal(err)
	}
	hub := testClaim("topic", "Hub")
	hub.Body = strings.Repeat("x", 4000)
	hub.Provenance = []Provenance{{Method: "document", Source: strings.Repeat("x", 500)}, {Method: "observed", Source: strings.Repeat("x", 500)}}
	central, err := SetClaim(root, hub)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 17; i++ {
		c := testClaim("topic", strings.Repeat("\x01", 200))
		c.Links = []Link{{Target: central.ID, Relation: "contradicts"}}
		leaf, err := SetClaim(root, c)
		if err != nil {
			t.Fatal(err)
		}
		if i < 16 {
			central.Links = append(central.Links, Link{Target: leaf.ID, Relation: "supports"})
		}
	}
	central, err = SetClaim(root, *central)
	if err != nil {
		t.Fatal(err)
	}
	full, err := GetClaim(root, central.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(full)
	if len(full.Links) != 16 || len(full.Backlinks) != 16 || !full.MoreBacklinks || utf8.RuneCount(encoded) > MaxResponseCharacters-256 {
		t.Fatalf("full linked claim exceeded retrieval contract: %d chars, %+v", utf8.RuneCount(encoded), full)
	}
}

func TestClaimValidationAndAtomicLinks(t *testing.T) {
	root := memoryRoot(t)
	if _, err := Set(root, "topic", ""); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*Claim){
		func(c *Claim) { c.Confidence = "certain" },
		func(c *Claim) { c.Kind = "invented" },
		func(c *Claim) { c.Status = "invented" },
		func(c *Claim) { c.Importance = "invented" },
		func(c *Claim) { c.Path = "missing" },
		func(c *Claim) { c.Path = "" },
		func(c *Claim) { c.Provenance = nil },
		func(c *Claim) { c.VerifiedAt = time.Now().Add(time.Hour).Format(time.RFC3339) },
		func(c *Claim) { c.ReviewAfter = "yesterday" },
		func(c *Claim) { c.Links = []Link{{Target: strings.Repeat("a", 32), Relation: "supports"}} },
		func(c *Claim) { c.Body = strings.Repeat("x", MaxBodyChars+1) },
		func(c *Claim) { c.Body = strings.Repeat("\x01", 2000) },
	} {
		c := testClaim("topic", "Validation")
		mutate(&c)
		if _, err := SetClaim(root, c); err == nil {
			t.Fatalf("invalid claim accepted: %+v", c)
		}
	}
	page, err := QueryClaims(root, Query{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("failed mutation left a claim: %+v %v", page, err)
	}
	valid, err := SetClaim(root, testClaim("topic", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	valid.Body = "should not persist"
	valid.Links = []Link{{Target: strings.Repeat("a", 32), Relation: "contradicts"}}
	if _, err := SetClaim(root, *valid); err == nil {
		t.Fatal("missing link target accepted")
	}
	after, err := GetClaim(root, valid.ID)
	if err != nil || after.Revision != 1 || after.Body == "should not persist" {
		t.Fatalf("failed link update not atomic: %+v %v", after, err)
	}
}

func TestClaimsUpgradeExistingNoteDatabase(t *testing.T) {
	root := memoryRoot(t)
	db, err := sql.Open("sqlite3", DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE memory_nodes (
		path TEXT PRIMARY KEY COLLATE BINARY,title TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',body TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1);
		INSERT INTO memory_nodes(path,body,revision) VALUES ('topic','Original unqualified note',7)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	claim, err := SetClaim(root, testClaim("topic", "New qualified record"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := Get(root, "topic")
	if err != nil || node.Body != "Original unqualified note" || node.Revision != 7 {
		t.Fatalf("upgrade changed notes: %+v %v", node, err)
	}
	if claim.Provenance[0].Source != "design.md" || IsEmpty(root) {
		t.Fatal("structured memory not preserved")
	}
}

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
	if _, err := Set(memoryRoot(t), strings.Repeat("short/", MaxPathChars), "body"); err == nil {
		t.Fatal("new note accepted a path too large for bounded retrieval")
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
