package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCategoryDisplayDefaultPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hem.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := s.CategoryDefaults()
	if err != nil || len(defaults) != 0 {
		t.Fatalf("new category defaults = %v, %v", defaults, err)
	}
	on, off := true, false
	for _, name := range []string{"Work", ""} {
		if err := s.SetCategoryDefault(name, &off, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate the pre-override schema and preserve its Display values.
	if _, err := s.db.Exec("ALTER TABLE project_category_defaults DROP COLUMN override_filter"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defaults, err = s.CategoryDefaults()
	if err != nil || len(defaults) != 2 || defaults["Work"].Display || defaults[""].Display || defaults["Work"].OverrideFilter {
		t.Fatalf("persisted category defaults = %v, %v", defaults, err)
	}
	if err := s.SetCategoryDefault("Work", nil, &on); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategoryDefault("Work", &on, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategoryDefault("Background", nil, &on); err != nil {
		t.Fatal(err)
	}
	defaults, err = s.CategoryDefaults()
	if err != nil || !defaults["Work"].Display || !defaults["Work"].OverrideFilter || defaults[""].Display ||
		!defaults["Background"].Display || !defaults["Background"].OverrideFilter {
		t.Fatalf("updated category defaults = %v, %v", defaults, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defaults, err = s.CategoryDefaults()
	if err != nil || !defaults["Work"].OverrideFilter {
		t.Fatalf("override did not persist: %v, %v", defaults, err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestProjectCategoryMigrationAndUpdates(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	if err := s.CreateProject(&Project{ID: "legacy", Name: "Legacy", Status: "active", Paths: "[]", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE projects DROP COLUMN category"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.migrateSchema(); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.GetProject("Legacy")
	if err != nil || p == nil || p.Category != "" || p.Status != "active" {
		t.Fatalf("migration lost legacy project: %+v, %v", p, err)
	}
	for _, category := range []string{"Work", ""} {
		if err := s.UpdateProject(p.ID, nil, nil, nil, nil, nil, nil, &category); err != nil {
			t.Fatal(err)
		}
		projects, err := s.ListProjects("active")
		if err != nil || len(projects) != 1 || projects[0].Category != category {
			t.Fatalf("category update: %+v, %v", projects, err)
		}
	}
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

func TestMoneypennyMI6FingerprintPersists(t *testing.T) {
	s := newTestStore(t)
	mp := &Moneypenny{
		Name:                 "remote",
		TransportType:        TransportMI6,
		MI6Addr:              "mi6.example:7007/remote",
		MI6ServerFingerprint: "SHA256:trusted",
	}

	if err := s.AddMoneypenny(mp); err != nil {
		t.Fatalf("AddMoneypenny: %v", err)
	}
	got, err := s.GetMoneypenny("remote")
	if err != nil {
		t.Fatalf("GetMoneypenny: %v", err)
	}
	if got.MI6ServerFingerprint != mp.MI6ServerFingerprint {
		t.Fatalf("MI6ServerFingerprint = %q, want %q", got.MI6ServerFingerprint, mp.MI6ServerFingerprint)
	}
}

func TestProcessScheduleReadyOnlySurfacesNewResults(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddMoneypenny(&Moneypenny{
		Name:          "mp",
		TransportType: TransportFIFO,
		FIFOIn:        "/tmp/in",
		FIFOOut:       "/tmp/out",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackSession("scheduled", "mp"); err != nil {
		t.Fatal(err)
	}
	readyAt := time.Now().UTC().Truncate(time.Second)
	surfaced, err := s.ProcessScheduleReady("scheduled", readyAt)
	if err != nil || !surfaced {
		t.Fatalf("first marker = (%v, %v), want (true, nil)", surfaced, err)
	}
	sess, err := s.GetSession("scheduled")
	if err != nil || sess == nil || sess.Reviewed {
		t.Fatalf("scheduled session should be unreviewed: %+v, %v", sess, err)
	}
	if err := s.SetSessionReviewed("scheduled", true); err != nil {
		t.Fatal(err)
	}
	surfaced, err = s.ProcessScheduleReady("scheduled", readyAt)
	if err != nil || surfaced {
		t.Fatalf("same marker = (%v, %v), want (false, nil)", surfaced, err)
	}
	surfaced, err = s.ProcessScheduleReady("scheduled", readyAt.Add(time.Second))
	if err != nil || !surfaced {
		t.Fatalf("newer marker = (%v, %v), want (true, nil)", surfaced, err)
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

func TestRenameMoneypennyPreservesReferences(t *testing.T) {
	s := newTestStore(t)
	old := &Moneypenny{Name: "old", TransportType: TransportMI6, MI6Addr: "relay/session", MI6ServerFingerprint: "SHA256:pin", Enabled: true}
	if err := s.AddMoneypenny(old); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMoneypenny(&Moneypenny{Name: "taken", TransportType: TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultMoneypenny("old"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("moneypenny", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackSession("session", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateProject(&Project{ID: "project", Name: "Project", Moneypenny: "old", Paths: "[]"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCachedModels("old", "opencode", []CachedModelEntry{{Name: "model", Value: "azure/model"}}); err != nil {
		t.Fatal(err)
	}
	shared := Ponsonby{Moneypenny: "old"}
	shared.ID, shared.Name = "engineering", "engineering"
	if err := s.SavePonsonby(shared); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][2]string{{"missing", "new"}, {"old", "taken"}, {"old", " "}, {"old", "old"}} {
		if err := s.RenameMoneypenny(names[0], names[1]); err == nil {
			t.Fatalf("rename %q to %q unexpectedly succeeded", names[0], names[1])
		}
	}
	if mp, err := s.GetMoneypenny("old"); err != nil || mp == nil {
		t.Fatalf("failed renames modified registration: %+v, %v", mp, err)
	}
	if err := s.RenameMoneypenny("old", "new"); err != nil {
		t.Fatal(err)
	}
	mp, err := s.GetDefaultMoneypenny()
	if err != nil || mp == nil || mp.Name != "new" || mp.MI6Addr != old.MI6Addr || mp.MI6ServerFingerprint != old.MI6ServerFingerprint || !mp.Enabled {
		t.Fatalf("renamed default = %+v, %v", mp, err)
	}
	if name, err := s.GetSessionMoneypenny("session"); err != nil || name != "new" {
		t.Fatalf("tracked session host = %q, %v", name, err)
	}
	if p, err := s.GetProject("project"); err != nil || p == nil || p.Moneypenny != "new" {
		t.Fatalf("project = %+v, %v", p, err)
	}
	if models, _, err := s.GetCachedModels("new", "opencode"); err != nil || len(models) != 1 || models[0].Value != "azure/model" {
		t.Fatalf("renamed models = %+v, %v", models, err)
	}
	if value, err := s.GetDefault("moneypenny"); err != nil || value != "new" {
		t.Fatalf("named default = %q, %v", value, err)
	}
	if stores, err := s.ListPonsonbies(); err != nil || len(stores) != 1 || stores[0].Moneypenny != "new" {
		t.Fatal("shared store lost renamed host", stores, err)
	}
	if err := s.DeleteMoneypenny("old"); err != nil {
		t.Fatal(err)
	}
	if name, err := s.GetSessionMoneypenny("session"); err != nil || name != "new" {
		t.Fatalf("old registration delete cascaded to renamed session: %q, %v", name, err)
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

func TestSetSessionParent(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddMoneypenny(&Moneypenny{Name: "mp1", TransportType: TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "child"} {
		if err := s.TrackSession(id, "mp1"); err != nil {
			t.Fatalf("TrackSession(%q): %v", id, err)
		}
	}
	if err := s.SetSessionParent("child", "parent"); err != nil {
		t.Fatalf("SetSessionParent: %v", err)
	}
	child, err := s.GetSession("child")
	if err != nil || child.ParentSessionID != "parent" {
		t.Fatalf("parent = %q, err = %v; want parent", child.ParentSessionID, err)
	}
	if err := s.SetSessionParent("child", ""); err != nil {
		t.Fatalf("promote: %v", err)
	}
	child, err = s.GetSession("child")
	if err != nil || child.ParentSessionID != "" {
		t.Fatalf("parent = %q, err = %v; want empty", child.ParentSessionID, err)
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

func TestTraitEnabledByDefault(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()

	if err := s.CreateTrait(&Trait{ID: "t1", Name: "alpha", Prompt: "a", EnabledByDefault: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTrait t1: %v", err)
	}
	if err := s.CreateTrait(&Trait{ID: "t2", Name: "beta", Prompt: "b", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateTrait t2: %v", err)
	}

	got, err := s.GetTrait("t1")
	if err != nil || got == nil {
		t.Fatalf("GetTrait t1: %v", err)
	}
	if !got.EnabledByDefault {
		t.Fatalf("t1 should be enabled by default")
	}

	got2, _ := s.GetTrait("t2")
	if got2.EnabledByDefault {
		t.Fatalf("t2 should not be enabled by default")
	}

	// Toggle t1 off and t2 on via UpdateTrait.
	off, on := false, true
	if err := s.UpdateTrait("t1", nil, nil, &off); err != nil {
		t.Fatalf("UpdateTrait t1: %v", err)
	}
	if err := s.UpdateTrait("t2", nil, nil, &on); err != nil {
		t.Fatalf("UpdateTrait t2: %v", err)
	}

	// nil enabledByDefault must leave the flag unchanged.
	newName := "beta2"
	if err := s.UpdateTrait("t2", &newName, nil, nil); err != nil {
		t.Fatalf("UpdateTrait t2 name: %v", err)
	}

	traits, err := s.ListTraits()
	if err != nil {
		t.Fatalf("ListTraits: %v", err)
	}
	byID := map[string]*Trait{}
	for _, tr := range traits {
		byID[tr.ID] = tr
	}
	if byID["t1"].EnabledByDefault {
		t.Fatalf("t1 should now be disabled")
	}
	if !byID["t2"].EnabledByDefault {
		t.Fatalf("t2 should now be enabled and unchanged by name update")
	}
	if byID["t2"].Name != "beta2" {
		t.Fatalf("t2 name not updated: %q", byID["t2"].Name)
	}
}

func TestVisibleAcknowledgementIsMonotonicAndGenerationScoped(t *testing.T) {
	s := newTestStore(t)
	if err := s.AddMoneypenny(&Moneypenny{Name: "mp", TransportType: TransportFIFO, FIFOIn: "/tmp/in", FIFOOut: "/tmp/out"}); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackSession("s1", "mp"); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveVisible("s1", 4, 2); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.AcknowledgeVisible("s1", 4, 2); err != nil || !changed {
		t.Fatalf("initial ack changed=%v err=%v", changed, err)
	}
	if err := s.SetSessionReviewed("s1", false); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.AcknowledgeVisible("s1", 3, 1); err != nil || changed {
		t.Fatalf("stale generation changed=%v err=%v", changed, err)
	}
	if changed, err := s.AcknowledgeVisible("s1", 2, 2); err != nil || changed {
		t.Fatalf("lower revision changed=%v err=%v", changed, err)
	}
	if err := s.ObserveVisible("s1", 1, 3); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.AcknowledgeVisible("s1", 1, 3); err != nil || !changed {
		t.Fatalf("new generation changed=%v err=%v", changed, err)
	}
	got, err := s.GetSession("s1")
	if err != nil || !got.Reviewed {
		t.Fatalf("reviewed=%v err=%v", got.Reviewed, err)
	}
}

func TestMainSessionResultReadinessPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hem.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	if err := s.AddMoneypenny(&Moneypenny{Name: "mp", TransportType: TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"main", "child", "completed"} {
		if err := s.TrackSession(id, "mp"); err != nil {
			t.Fatal(err)
		}
		if err := s.ObserveVisible(id, 4, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AcknowledgeVisible(id, 4, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSessionParent("child", "main"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionHemStatus("completed", "completed"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"main", "child", "completed"} {
		changed, err := s.ProcessSessionResult(id, "mp", 7, 1)
		if err != nil || changed != (id == "main") {
			t.Fatalf("process %s: changed=%v err=%v", id, changed, err)
		}
		session, err := s.GetSession(id)
		if err != nil || session.Reviewed != (id != "main") {
			t.Fatalf("readiness %s: %+v, %v", id, session, err)
		}
	}
	if changed, err := s.AcknowledgeVisible("main", 4, 1); err != nil || changed {
		t.Fatalf("working-transcript ack cleared the new result: %v, %v", changed, err)
	}
	if err := s.ObserveVisible("main", 8, 1); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetSession("main")
	if err != nil || session.Reviewed {
		t.Fatalf("passive history read cleared the result: %+v, %v", session, err)
	}
	if changed, err := s.AcknowledgeVisible("main", 8, 1); err != nil || !changed {
		t.Fatalf("rendered result ack: %v, %v", changed, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{0, 6, 7} {
		if changed, err := s.ProcessSessionResult("main", "mp", revision, 1); err != nil || changed {
			t.Fatalf("stale/duplicate result reopened after restart: %d %v %v", revision, changed, err)
		}
	}
	if changed, err := s.ProcessSessionResult("main", "other-host", 9, 1); err != nil || changed {
		t.Fatalf("wrong host result accepted: %v, %v", changed, err)
	}
	// A user can render the final answer before the idle/list snapshot arrives.
	if err := s.ObserveVisible("main", 12, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcknowledgeVisible("main", 12, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessSessionResult("main", "mp", 11, 1); err != nil {
		t.Fatal(err)
	}
	session, err = s.GetSession("main")
	if err != nil || !session.Reviewed {
		t.Fatalf("already-rendered result reopened: %+v, %v", session, err)
	}
	if changed, err := s.ProcessSessionResult("main", "mp", 1, 2); err != nil || !changed {
		t.Fatalf("new generation result: %v, %v", changed, err)
	}
	if changed, err := s.AcknowledgeVisible("main", 12, 1); err != nil || changed {
		t.Fatalf("old generation ack cleared result: %v, %v", changed, err)
	}
}
