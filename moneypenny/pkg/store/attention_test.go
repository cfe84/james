package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestSessionAttentionLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			_ = s.Close()
		}
	})
	if err := s.CreateSession(&Session{SessionID: "session", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	kind, summary := "needs_answer", "Comparing deployment options"
	report, changed, err := s.SetSessionReport("session", envelope.AttentionUpdate{
		Kind: &kind, Reason: "Which host?", Summary: &summary,
	}, "agent")
	if err != nil || !changed || report.Attention.Revision != 1 {
		t.Fatalf("initial report: %+v changed=%v err=%v", report, changed, err)
	}
	before, _ := s.GetSession("session")
	repeated, changed, err := s.SetSessionReport("session", envelope.AttentionUpdate{Kind: &kind, Reason: "Which host?"}, "agent")
	after, _ := s.GetSession("session")
	if err != nil || changed || !reflect.DeepEqual(repeated, report) || before.Revision != after.Revision {
		t.Fatalf("duplicate request changed state: %+v changed=%v err=%v", repeated, changed, err)
	}
	summary = "Deployment options ready"
	report, changed, err = s.SetSessionReport("session", envelope.AttentionUpdate{Summary: &summary}, "agent")
	if err != nil || changed || report.Attention.Revision != 1 {
		t.Fatalf("summary cleared or re-alerted request: %+v %v", report, err)
	}
	if err := s.AddConversationTurn("session", "user", "Thanks"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus("session", StateIdle); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.GetSession("session")
	list, listErr := s.ListSessions()
	if err != nil || listErr != nil || len(list) != 1 ||
		!reflect.DeepEqual(persisted.SessionReport, report) || !reflect.DeepEqual(list[0].SessionReport, report) {
		t.Fatalf("request did not survive reply/completion/reopen: get=%+v list=%+v err=%v/%v", persisted, list, err, listErr)
	}
	report, _, err = s.SetSessionReport("session", envelope.AttentionUpdate{Kind: &kind, Reason: "Which region?"}, "agent")
	if err != nil || report.Attention.Revision != 2 {
		t.Fatalf("changed request: %+v err=%v", report, err)
	}
	resolve, stale := "", int64(1)
	if _, _, err := s.SetSessionReport("session", envelope.AttentionUpdate{Kind: &resolve, Revision: &stale}, "operator"); err == nil {
		t.Fatal("stale resolution dismissed newer question")
	}
	current := report.Attention.Revision
	report, changed, err = s.SetSessionReport("session", envelope.AttentionUpdate{Kind: &resolve, Revision: &current}, "operator")
	if err != nil || !changed || report.Attention.Active() || report.Attention.Revision != 3 || report.Summary != summary {
		t.Fatalf("explicit resolution: %+v changed=%v err=%v", report, changed, err)
	}
	if err := s.UpdateSessionStatus("session", StateWorking); err != nil {
		t.Fatal(err)
	}
	if count, err := s.ResetWorkingSessions(); err != nil || count != 1 {
		t.Fatalf("restart recovery count=%d err=%v", count, err)
	}
	recovered, _ := s.GetSession("session")
	if recovered.Attention.Kind != "failed" || recovered.Attention.Revision != 4 ||
		recovered.Attention.Source != "daemon" || recovered.Summary != summary {
		t.Fatalf("restart recovery lost report: %+v", recovered.SessionReport)
	}
	if count, err := s.ResetWorkingSessions(); err != nil || count != 0 {
		t.Fatalf("restart recovery repeated count=%d err=%v", count, err)
	}
}
