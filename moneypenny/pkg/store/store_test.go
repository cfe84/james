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

func TestCreateAndGetSession(t *testing.T) {
	s := newTestStore(t)

	sess := &Session{
		SessionID:    "s1",
		Name:         "Test Session",
		Agent:        "claude",
		SystemPrompt: "You are helpful.",
		Yolo:         true,
		Path:         "/tmp/work",
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := s.GetSession("s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil {
		t.Fatal("GetSession returned nil")
	}
	if got.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "s1")
	}
	if got.Name != "Test Session" {
		t.Errorf("Name = %q, want %q", got.Name, "Test Session")
	}
	if got.Agent != "claude" {
		t.Errorf("Agent = %q, want %q", got.Agent, "claude")
	}
	if got.SystemPrompt != "You are helpful." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You are helpful.")
	}
	if !got.Yolo {
		t.Error("Yolo = false, want true")
	}
	if got.Path != "/tmp/work" {
		t.Errorf("Path = %q, want %q", got.Path, "/tmp/work")
	}
	if got.Status != StateIdle {
		t.Errorf("Status = %q, want %q", got.Status, StateIdle)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
}

func TestCreateSessionDuplicateID(t *testing.T) {
	s := newTestStore(t)

	sess := &Session{SessionID: "dup", Name: "A", Agent: "a"}
	if err := s.CreateSession(sess); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}

	sess2 := &Session{SessionID: "dup", Name: "B", Agent: "b"}
	if err := s.CreateSession(sess2); err == nil {
		t.Fatal("expected error for duplicate session_id, got nil")
	}
}

func TestListSessions(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []string{"a", "b", "c"} {
		if err := s.CreateSession(&Session{SessionID: id, Name: id, Agent: "agent"}); err != nil {
			t.Fatalf("CreateSession(%s): %v", id, err)
		}
	}

	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want 3", len(sessions))
	}
}

func TestUpdateSessionStatus(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession(&Session{SessionID: "u1", Name: "n", Agent: "a"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.UpdateSessionStatus("u1", StateWorking); err != nil {
		t.Fatalf("UpdateSessionStatus: %v", err)
	}

	got, err := s.GetSession("u1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Status != StateWorking {
		t.Errorf("Status = %q, want %q", got.Status, StateWorking)
	}
}

func TestDeleteSessionAlsoDeletesConversation(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession(&Session{SessionID: "d1", Name: "n", Agent: "a"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.AddConversationTurn("d1", "user", "hello"); err != nil {
		t.Fatalf("AddConversationTurn: %v", err)
	}
	if err := s.AddConversationTurn("d1", "assistant", "hi"); err != nil {
		t.Fatalf("AddConversationTurn: %v", err)
	}

	if err := s.DeleteSession("d1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got, err := s.GetSession("d1")
	if err != nil {
		t.Fatalf("GetSession after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil session after delete")
	}

	turns, err := s.GetConversation("d1")
	if err != nil {
		t.Fatalf("GetConversation after delete: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("expected 0 turns after delete, got %d", len(turns))
	}
}

func TestAddAndGetConversationTurns(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateSession(&Session{SessionID: "c1", Name: "n", Agent: "a"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	messages := []struct {
		role    string
		content string
	}{
		{"user", "first message"},
		{"assistant", "first reply"},
		{"user", "second message"},
		{"assistant", "second reply"},
	}

	for _, m := range messages {
		if err := s.AddConversationTurn("c1", m.role, m.content); err != nil {
			t.Fatalf("AddConversationTurn(%s, %s): %v", m.role, m.content, err)
		}
	}

	turns, err := s.GetConversation("c1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("len(turns) = %d, want 4", len(turns))
	}

	for i, m := range messages {
		if turns[i].Role != m.role {
			t.Errorf("turn[%d].Role = %q, want %q", i, turns[i].Role, m.role)
		}
		if turns[i].Content != m.content {
			t.Errorf("turn[%d].Content = %q, want %q", i, turns[i].Content, m.content)
		}
		if turns[i].SessionID != "c1" {
			t.Errorf("turn[%d].SessionID = %q, want %q", i, turns[i].SessionID, "c1")
		}
	}

	// Verify ordering: IDs should be ascending.
	for i := 1; i < len(turns); i++ {
		if turns[i].ID <= turns[i-1].ID {
			t.Errorf("turns not ordered: turn[%d].ID=%d <= turn[%d].ID=%d", i, turns[i].ID, i-1, turns[i-1].ID)
		}
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetSession("nonexistent")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-existent session, got %+v", got)
	}
}
