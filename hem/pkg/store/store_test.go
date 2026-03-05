package store

import (
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddAndGetMoneypenny(t *testing.T) {
	s := newTestStore(t)

	mp := &Moneypenny{
		Name:          "mp1",
		TransportType: TransportFIFO,
		FIFOIn:        "/tmp/in",
		FIFOOut:       "/tmp/out",
	}
	if err := s.AddMoneypenny(mp); err != nil {
		t.Fatalf("AddMoneypenny: %v", err)
	}

	got, err := s.GetMoneypenny("mp1")
	if err != nil {
		t.Fatalf("GetMoneypenny: %v", err)
	}
	if got == nil {
		t.Fatal("GetMoneypenny returned nil")
	}
	if got.Name != "mp1" || got.TransportType != TransportFIFO || got.FIFOIn != "/tmp/in" || got.FIFOOut != "/tmp/out" {
		t.Errorf("unexpected moneypenny: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
}

func TestGetMoneypennyNotFound(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetMoneypenny("nonexistent")
	if err != nil {
		t.Fatalf("GetMoneypenny: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestDuplicateNameErrors(t *testing.T) {
	s := newTestStore(t)

	mp := &Moneypenny{Name: "dup", TransportType: TransportFIFO}
	if err := s.AddMoneypenny(mp); err != nil {
		t.Fatalf("first AddMoneypenny: %v", err)
	}
	if err := s.AddMoneypenny(mp); err == nil {
		t.Fatal("expected error on duplicate name, got nil")
	}
}

func TestListMoneypennies(t *testing.T) {
	s := newTestStore(t)

	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := s.AddMoneypenny(&Moneypenny{Name: name, TransportType: TransportFIFO}); err != nil {
			t.Fatalf("AddMoneypenny(%s): %v", name, err)
		}
	}

	list, err := s.ListMoneypennies()
	if err != nil {
		t.Fatalf("ListMoneypennies: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3, got %d", len(list))
	}
	// Should be sorted by name.
	if list[0].Name != "alpha" || list[1].Name != "bravo" || list[2].Name != "charlie" {
		t.Errorf("unexpected order: %s, %s, %s", list[0].Name, list[1].Name, list[2].Name)
	}
}

func TestDeleteMoneypenny(t *testing.T) {
	s := newTestStore(t)

	if err := s.AddMoneypenny(&Moneypenny{Name: "del", TransportType: TransportFIFO}); err != nil {
		t.Fatalf("AddMoneypenny: %v", err)
	}
	if err := s.DeleteMoneypenny("del"); err != nil {
		t.Fatalf("DeleteMoneypenny: %v", err)
	}
	got, err := s.GetMoneypenny("del")
	if err != nil {
		t.Fatalf("GetMoneypenny: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestSetAndGetDefault(t *testing.T) {
	s := newTestStore(t)

	// No default initially.
	def, err := s.GetDefaultMoneypenny()
	if err != nil {
		t.Fatalf("GetDefaultMoneypenny: %v", err)
	}
	if def != nil {
		t.Fatalf("expected nil default, got %+v", def)
	}

	// Add two and set default.
	s.AddMoneypenny(&Moneypenny{Name: "a", TransportType: TransportFIFO})
	s.AddMoneypenny(&Moneypenny{Name: "b", TransportType: TransportMI6, MI6Addr: "host/123"})

	if err := s.SetDefaultMoneypenny("a"); err != nil {
		t.Fatalf("SetDefaultMoneypenny(a): %v", err)
	}
	def, err = s.GetDefaultMoneypenny()
	if err != nil {
		t.Fatalf("GetDefaultMoneypenny: %v", err)
	}
	if def == nil || def.Name != "a" {
		t.Fatalf("expected default=a, got %+v", def)
	}

	// Switch default.
	if err := s.SetDefaultMoneypenny("b"); err != nil {
		t.Fatalf("SetDefaultMoneypenny(b): %v", err)
	}
	def, _ = s.GetDefaultMoneypenny()
	if def == nil || def.Name != "b" {
		t.Fatalf("expected default=b, got %+v", def)
	}

	// Old one should no longer be default.
	a, _ := s.GetMoneypenny("a")
	if a.IsDefault {
		t.Error("a should no longer be default")
	}
}

func TestSetDefaultNonexistent(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetDefaultMoneypenny("nope"); err == nil {
		t.Fatal("expected error for nonexistent moneypenny")
	}
}

func TestTrackAndLookupSession(t *testing.T) {
	s := newTestStore(t)
	s.AddMoneypenny(&Moneypenny{Name: "mp1", TransportType: TransportFIFO})

	if err := s.TrackSession("sess1", "mp1"); err != nil {
		t.Fatalf("TrackSession: %v", err)
	}

	name, err := s.GetSessionMoneypenny("sess1")
	if err != nil {
		t.Fatalf("GetSessionMoneypenny: %v", err)
	}
	if name != "mp1" {
		t.Fatalf("expected mp1, got %q", name)
	}

	// Not found.
	name, err = s.GetSessionMoneypenny("nonexistent")
	if err != nil {
		t.Fatalf("GetSessionMoneypenny: %v", err)
	}
	if name != "" {
		t.Fatalf("expected empty, got %q", name)
	}
}

func TestListSessionsWithFilter(t *testing.T) {
	s := newTestStore(t)
	s.AddMoneypenny(&Moneypenny{Name: "mp1", TransportType: TransportFIFO})
	s.AddMoneypenny(&Moneypenny{Name: "mp2", TransportType: TransportMI6})

	s.TrackSession("s1", "mp1")
	s.TrackSession("s2", "mp1")
	s.TrackSession("s3", "mp2")

	// All sessions.
	all, err := s.ListTrackedSessions("")
	if err != nil {
		t.Fatalf("ListTrackedSessions(''): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3, got %d", len(all))
	}

	// Filtered.
	filtered, err := s.ListTrackedSessions("mp1")
	if err != nil {
		t.Fatalf("ListTrackedSessions('mp1'): %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d", len(filtered))
	}
	for _, sess := range filtered {
		if sess.MoneypennyName != "mp1" {
			t.Errorf("expected mp1, got %q", sess.MoneypennyName)
		}
	}
}

func TestDeleteTrackedSession(t *testing.T) {
	s := newTestStore(t)
	s.AddMoneypenny(&Moneypenny{Name: "mp1", TransportType: TransportFIFO})
	s.TrackSession("s1", "mp1")

	if err := s.DeleteTrackedSession("s1"); err != nil {
		t.Fatalf("DeleteTrackedSession: %v", err)
	}

	name, _ := s.GetSessionMoneypenny("s1")
	if name != "" {
		t.Fatalf("expected empty after delete, got %q", name)
	}
}

func TestDeleteMoneypennyCascadesToSessions(t *testing.T) {
	s := newTestStore(t)
	s.AddMoneypenny(&Moneypenny{Name: "mp1", TransportType: TransportFIFO})
	s.TrackSession("s1", "mp1")
	s.TrackSession("s2", "mp1")

	if err := s.DeleteMoneypenny("mp1"); err != nil {
		t.Fatalf("DeleteMoneypenny: %v", err)
	}

	// Sessions should be gone.
	sessions, err := s.ListTrackedSessions("")
	if err != nil {
		t.Fatalf("ListTrackedSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after cascade delete, got %d", len(sessions))
	}
}
