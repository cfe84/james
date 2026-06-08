package store

import (
	"strings"
	"testing"
)

func newMemTestSession(t *testing.T, s *Store) string {
	t.Helper()
	sess := &Session{SessionID: "m1", Name: "Mem", Agent: "claude"}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return sess.SessionID
}

func TestMemoryNodeCRUD(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)

	if err := s.SetMemoryNode(sid, "project/git", "Git workflow", "How we branch", "Use feature branches."); err != nil {
		t.Fatalf("SetMemoryNode: %v", err)
	}

	// Ancestor "project" should have been auto-created.
	anc, err := s.GetMemoryNode(sid, "project")
	if err != nil || anc == nil {
		t.Fatalf("expected ancestor project to exist, got %v err=%v", anc, err)
	}

	n, err := s.GetMemoryNode(sid, "project/git")
	if err != nil || n == nil {
		t.Fatalf("GetMemoryNode: %v err=%v", n, err)
	}
	if n.Title != "Git workflow" || n.Body != "Use feature branches." {
		t.Fatalf("unexpected node: %+v", n)
	}

	// Replace keeps path, updates body.
	if err := s.SetMemoryNode(sid, "project/git", "Git", "branching", "Updated."); err != nil {
		t.Fatalf("replace: %v", err)
	}
	n, _ = s.GetMemoryNode(sid, "project/git")
	if n.Body != "Updated." {
		t.Fatalf("expected updated body, got %q", n.Body)
	}
}

func TestMemoryNodeNormalization(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	if err := s.SetMemoryNode(sid, "/project//conventions/", "", "", "x"); err != nil {
		t.Fatalf("SetMemoryNode: %v", err)
	}
	if n, _ := s.GetMemoryNode(sid, "project/conventions"); n == nil {
		t.Fatalf("expected normalized path project/conventions")
	}
	if err := s.SetMemoryNode(sid, "   ", "", "", "x"); err == nil {
		t.Fatalf("expected error for empty path")
	}
}

func TestMemoryBodyCap(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	big := strings.Repeat("a", MemoryMaxBodyLen+1)
	err := s.SetMemoryNode(sid, "topic", "", "", big)
	if err == nil {
		t.Fatalf("expected over-cap body to be rejected")
	}
	if !strings.Contains(err.Error(), "child nodes") {
		t.Fatalf("expected hierarchize hint, got %v", err)
	}
}

func TestMemoryDelete(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	s.SetMemoryNode(sid, "a/b/c", "", "", "deep")
	s.SetMemoryNode(sid, "a/b", "", "", "mid")

	// Non-recursive delete of a node with children must fail.
	if _, err := s.DeleteMemoryNode(sid, "a/b", false); err == nil {
		t.Fatalf("expected non-recursive delete with children to fail")
	}
	// Recursive delete removes subtree.
	n, err := s.DeleteMemoryNode(sid, "a/b", true)
	if err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
	if node, _ := s.GetMemoryNode(sid, "a/b/c"); node != nil {
		t.Fatalf("expected a/b/c gone")
	}
	if node, _ := s.GetMemoryNode(sid, "a"); node == nil {
		t.Fatalf("expected a to remain")
	}
}

func TestMemoryDeletePrefixExact(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	// Case-sensitive sibling that shares a case-insensitive prefix with "a".
	s.SetMemoryNode(sid, "a/x", "", "", "lower")
	s.SetMemoryNode(sid, "A/y", "", "", "upper")
	// "ab/z" shares the literal prefix "a" but is not a child of "a".
	s.SetMemoryNode(sid, "ab/z", "", "", "sibling")

	n, err := s.DeleteMemoryNode(sid, "a", true)
	if err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	// Should delete only "a" and "a/x" (2 nodes), not "A/y" or "ab/z".
	if n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
	if node, _ := s.GetMemoryNode(sid, "A/y"); node == nil {
		t.Fatalf("expected A/y to survive (case-sensitive)")
	}
	if node, _ := s.GetMemoryNode(sid, "ab/z"); node == nil {
		t.Fatalf("expected ab/z to survive (prefix is a/, not a)")
	}
}

func TestMemoryPathRejectsWildcards(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	for _, p := range []string{"a%b", "a_b", `a\b`} {
		if err := s.SetMemoryNode(sid, p, "", "", "x"); err == nil {
			t.Fatalf("expected path %q to be rejected", p)
		}
	}
}

func TestMemorySearchAndOutline(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	s.SetMemoryNode(sid, "project/git", "Git workflow", "branching model", "We rebase often.")
	s.SetMemoryNode(sid, "project/style", "Style", "formatting", "gofmt everything.")

	res, err := s.SearchMemoryNodes(sid, "rebase")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) != 1 || res[0].Path != "project/git" {
		t.Fatalf("unexpected search results: %+v", res)
	}

	// Title hit should outrank body-only hit.
	res, _ = s.SearchMemoryNodes(sid, "git")
	if len(res) == 0 || res[0].Path != "project/git" {
		t.Fatalf("expected project/git first, got %+v", res)
	}

	outline, err := s.MemoryOutline(sid)
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if !strings.Contains(outline, "project/git") || strings.Contains(outline, "rebase") {
		t.Fatalf("outline should list paths without bodies: %q", outline)
	}
}

func TestMigrateLegacyMemory(t *testing.T) {
	s := newTestStore(t)
	sid := newMemTestSession(t, s)
	if err := s.SetMemory(sid, "old flat note"); err != nil {
		t.Fatalf("SetMemory: %v", err)
	}
	imported, err := s.MigrateLegacyMemory(sid)
	if err != nil || !imported {
		t.Fatalf("expected import, got %v err=%v", imported, err)
	}
	n, _ := s.GetMemoryNode(sid, "notes")
	if n == nil || n.Body != "old flat note" {
		t.Fatalf("expected legacy note imported, got %+v", n)
	}
	// Idempotent: second call no-ops.
	again, _ := s.MigrateLegacyMemory(sid)
	if again {
		t.Fatalf("expected second migration to no-op")
	}
}
