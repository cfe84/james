package commands

import (
	"path/filepath"
	"testing"

	"james/hem/pkg/store"
)

func newHierarchyExecutor(t *testing.T) *Executor {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "hem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(s, "")
}

func TestRenameMoneypennyCommandRetainsDashboardAndTransport(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "old", TransportType: store.TransportFIFO, FIFOIn: "/tmp/in", FIFOOut: "/tmp/out"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.TrackSession("session", "old"); err != nil {
		t.Fatal(err)
	}
	mp, _ := e.store.GetMoneypenny("old")
	client := e.clientManager.GetClient(mp)
	e.cacheManager.UpdateMP("old", map[string]mpSessionInfo{"session": {SessionID: "session"}})
	e.clientManager.SetCooldown("old")
	if resp := e.Dispatch("rename", "moneypenny", []string{"-n", "old", "--new-name", "new"}); resp.Status != "ok" {
		t.Fatalf("rename failed: %s", resp.Message)
	}
	mp, _ = e.store.GetMoneypenny("new")
	if e.clientManager.GetClient(mp) != client || !e.clientManager.IsUnavailable("new") || e.clientManager.IsUnavailable("old") {
		t.Fatal("client or cooldown not transferred")
	}
	if _, ok := e.cacheManager.GetSnapshot()["new"]["session"]; !ok {
		t.Fatal("dashboard cache not transferred")
	}
	if _, ok := e.cacheManager.GetSnapshot()["old"]; ok {
		t.Fatal("old dashboard cache key remains")
	}
	if resp := e.Dispatch("rename", "moneypenny", []string{"-n", "old", "--new-name", "other"}); resp.Status != "error" {
		t.Fatal("renaming an unknown registration succeeded")
	}
}

func TestAdoptAndPromoteSession(t *testing.T) {
	e := newHierarchyExecutor(t)
	for _, name := range []string{"mp1", "mp2"} {
		if err := e.store.AddMoneypenny(&store.Moneypenny{Name: name, TransportType: store.TransportFIFO}); err != nil {
			t.Fatal(err)
		}
	}
	for id, mp := range map[string]string{"parent": "mp1", "child": "mp1", "other": "mp2"} {
		if err := e.store.TrackSession(id, mp); err != nil {
			t.Fatal(err)
		}
	}

	if resp := e.AdoptSession([]string{"child", "--parent", "parent"}); resp.Status != "ok" {
		t.Fatalf("adopt failed: %s", resp.Message)
	}
	child, _ := e.store.GetSession("child")
	if child.ParentSessionID != "parent" {
		t.Fatalf("parent = %q, want parent", child.ParentSessionID)
	}
	if resp := e.PromoteSession([]string{"child"}); resp.Status != "ok" {
		t.Fatalf("promote failed: %s", resp.Message)
	}
	child, _ = e.store.GetSession("child")
	if child.ParentSessionID != "" {
		t.Fatalf("parent = %q, want empty", child.ParentSessionID)
	}
	if resp := e.AdoptSession([]string{"child", "--parent", "other"}); resp.Status != "error" {
		t.Fatal("cross-Moneypenny adoption succeeded")
	}
}

func TestAdoptSessionRejectsCycles(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp1", TransportType: store.TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"grandparent", "parent", "child"} {
		if err := e.store.TrackSession(id, "mp1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.SetSessionParent("parent", "grandparent"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSessionParent("child", "parent"); err != nil {
		t.Fatal(err)
	}
	if resp := e.AdoptSession([]string{"grandparent", "--parent", "child"}); resp.Status != "error" {
		t.Fatal("cycle adoption succeeded")
	}
}
