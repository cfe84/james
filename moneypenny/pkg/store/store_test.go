package store

import (
	"database/sql"
	"math"
	"sync"
	"testing"

	"james/moneypenny/pkg/envelope"
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

func TestQueueJobCallbackClaimsIdleAndDeduplicatesAfterDrain(t *testing.T) {
	s := newTestStore(t)
	s.db.SetMaxOpenConns(1)
	if err := s.CreateSession(&Session{SessionID: "job-session", Name: "job", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.QueueJobCallback("job-session", "job-1", "finished")
	if err != nil || !claimed {
		t.Fatalf("claim idle: %v, %v", claimed, err)
	}
	claimed, err = s.QueueJobCallback("job-session", "job-2", "also finished")
	if err != nil || claimed {
		t.Fatalf("working session claimed twice: %v, %v", claimed, err)
	}
	group, err := s.DrainQueueGroup("job-session")
	if err != nil || len(group) != 2 || group[0].Source != "callback" {
		t.Fatalf("queued callbacks: %+v, %v", group, err)
	}
	if _, err = s.DrainQueueGroup("job-session"); err != nil {
		t.Fatal(err)
	}
	claimed, err = s.QueueJobCallback("job-session", "job-1", "finished")
	if err != nil || claimed {
		t.Fatalf("replay of drained callback claimed again: %v, %v", claimed, err)
	}
	session, err := s.GetSession("job-session")
	if err != nil || session.Status != StateIdle {
		t.Fatalf("duplicate changed idle state: %+v, %v", session, err)
	}
}

func TestClientOperationLifecycleAndConflict(t *testing.T) {
	s := newTestStore(t)
	s.db.SetMaxOpenConns(1)
	if err := s.CreateSession(&Session{SessionID: "s1", Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	first, existing, err := s.BeginClientOperation("s1", "op-1", "digest-a", map[string]any{"status": OperationAccepted})
	if err != nil || existing || first.Status != OperationAccepted {
		t.Fatalf("first reservation = %#v existing=%v err=%v", first, existing, err)
	}
	replay, existing, err := s.BeginClientOperation("s1", "op-1", "digest-a", map[string]any{"status": OperationAccepted})
	if err != nil || !existing || string(replay.Response) != string(first.Response) {
		t.Fatalf("replay = %#v existing=%v err=%v", replay, existing, err)
	}
	if _, _, err := s.BeginClientOperation("s1", "op-1", "digest-b", nil); err == nil {
		t.Fatal("conflicting retry unexpectedly succeeded")
	}
	if ok, err := s.UpdateClientOperationStatus("s1", "op-1", OperationAccepted, OperationRunning, nil); err != nil || !ok {
		t.Fatalf("accepted -> running: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UpdateClientOperationStatus("s1", "op-1", OperationAccepted, OperationQueued, nil); err != nil || ok {
		t.Fatalf("stale transition changed operation: ok=%v err=%v", ok, err)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, already, err := s.BeginClientOperation("s1", "op-concurrent", "same", map[string]any{"accepted": true})
			results <- err == nil && !already
		}()
	}
	wg.Wait()
	close(results)
	var inserted int
	for result := range results {
		if result {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("concurrent reservations inserted %d rows, want 1", inserted)
	}
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
		Environment:  `{"PLAYWRIGHT_MCP_EXTENSION_TOKEN":"test-token"}`,
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
	if got.CompactionThresholdTokens != envelope.DefaultCompactionThresholdTokens {
		t.Errorf("CompactionThresholdTokens = %d, want %d", got.CompactionThresholdTokens, envelope.DefaultCompactionThresholdTokens)
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
	if got.Environment != `{"PLAYWRIGHT_MCP_EXTENSION_TOKEN":"test-token"}` {
		t.Errorf("Environment = %q, want stored value", got.Environment)
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

func TestAddOpenCodeCost(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "s-cost", Name: "Cost", Agent: "opencode"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.AddOpenCodeCost("s-cost", 0.0123); err != nil {
		t.Fatalf("AddOpenCodeCost: %v", err)
	}
	if err := s.AddOpenCodeCost("s-cost", 0.0045); err != nil {
		t.Fatalf("AddOpenCodeCost: %v", err)
	}
	got, err := s.GetSession("s-cost")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if math.Abs(got.OpenCodeCost-0.0168) > 1e-9 {
		t.Errorf("OpenCodeCost = %v, want 0.0168", got.OpenCodeCost)
	}
}

func TestCompactionThresholdValidationAndPersistence(t *testing.T) {
	s := newTestStore(t)
	for _, threshold := range []int{envelope.MinCompactionThresholdTokens, envelope.MaxCompactionThresholdTokens} {
		id := "threshold-" + string(rune('0'+threshold/envelope.MinCompactionThresholdTokens))
		if err := s.CreateSession(&Session{
			SessionID: id, Name: id, Agent: "copilot",
			CompactionThresholdTokens: threshold,
		}); err != nil {
			t.Fatalf("CreateSession(%d): %v", threshold, err)
		}
		got, err := s.GetSession(id)
		if err != nil {
			t.Fatalf("GetSession(%d): %v", threshold, err)
		}
		if got.CompactionThresholdTokens != threshold {
			t.Errorf("threshold = %d, want %d", got.CompactionThresholdTokens, threshold)
		}
		if err := s.UpdateSessionFieldsWithCompactionThresholdTokens(id, nil, nil, nil, nil, nil, nil, nil, &threshold, nil, nil, nil); err != nil {
			t.Fatalf("UpdateSessionFieldsWithCompactionThresholdTokens(%d): %v", threshold, err)
		}
	}
	if err := s.CreateSession(&Session{SessionID: "legacy", Name: "legacy", Agent: "copilot"}); err != nil {
		t.Fatalf("CreateSession(legacy): %v", err)
	}
	if _, err := s.db.Exec("UPDATE sessions SET compaction_threshold_tokens = 0 WHERE session_id = 'legacy'"); err != nil {
		t.Fatalf("simulate legacy zero threshold: %v", err)
	}
	legacy, err := s.GetSession("legacy")
	if err != nil {
		t.Fatalf("GetSession(legacy): %v", err)
	}
	if legacy.CompactionThresholdTokens != envelope.DefaultCompactionThresholdTokens {
		t.Fatalf("legacy threshold = %d, want %d", legacy.CompactionThresholdTokens, envelope.DefaultCompactionThresholdTokens)
	}
	for _, threshold := range []int{envelope.MinCompactionThresholdTokens - 1, envelope.MaxCompactionThresholdTokens + 1} {
		if err := s.CreateSession(&Session{
			SessionID: "invalid-" + string(rune('0'+threshold%10)), Name: "invalid", Agent: "copilot",
			CompactionThresholdTokens: threshold,
		}); err == nil {
			t.Errorf("CreateSession(%d) succeeded, want validation error", threshold)
		}
		invalid := threshold
		if err := s.UpdateSessionFieldsWithCompactionThresholdTokens("threshold-5", nil, nil, nil, nil, nil, nil, nil, &invalid, nil, nil, nil); err == nil {
			t.Errorf("UpdateSessionFieldsWithCompactionThresholdTokens(%d) succeeded, want validation error", threshold)
		}
	}
}

func TestCompactionThresholdDefaultsFollowContextAndUpdatesPreserveExplicitValue(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{
		SessionID: "long-default", Name: "long", Agent: "copilot", ContextTier: "long_context",
	}); err != nil {
		t.Fatalf("CreateSession(long): %v", err)
	}
	long, err := s.GetSession("long-default")
	if err != nil {
		t.Fatal(err)
	}
	if long.CompactionThresholdTokens != envelope.LongContextCompactionThresholdTokens {
		t.Fatalf("long context threshold = %d, want %d", long.CompactionThresholdTokens, envelope.LongContextCompactionThresholdTokens)
	}

	normalTier := ""
	if err := s.UpdateSessionFieldsWithCompactionThresholdTokens("long-default", nil, nil, nil, nil, &normalTier, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("clear context tier: %v", err)
	}
	updated, err := s.GetSession("long-default")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ContextTier != "" || updated.CompactionThresholdTokens != envelope.LongContextCompactionThresholdTokens {
		t.Fatalf("context update overwrote threshold: %+v", updated)
	}
}

func TestCompactionThresholdMigrationDerivesLongContextOnlyOnce(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			session_id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			agent TEXT NOT NULL,
			context_tier TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO sessions(session_id, name, agent, context_tier) VALUES
			('legacy-long', 'long', 'copilot', 'long_context'),
			('legacy-normal', 'normal', 'copilot', '');
	`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	var long, normal int
	if err := db.QueryRow(`SELECT compaction_threshold_tokens FROM sessions WHERE session_id = 'legacy-long'`).Scan(&long); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT compaction_threshold_tokens FROM sessions WHERE session_id = 'legacy-normal'`).Scan(&normal); err != nil {
		t.Fatal(err)
	}
	if long != envelope.LongContextCompactionThresholdTokens || normal != envelope.DefaultCompactionThresholdTokens {
		t.Fatalf("migration defaults = long %d normal %d", long, normal)
	}
	if _, err := db.Exec(`UPDATE sessions SET compaction_threshold_tokens = ? WHERE session_id = 'legacy-long'`, envelope.MinCompactionThresholdTokens); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if err := db.QueryRow(`SELECT compaction_threshold_tokens FROM sessions WHERE session_id = 'legacy-long'`).Scan(&long); err != nil {
		t.Fatal(err)
	}
	if long != envelope.MinCompactionThresholdTokens {
		t.Fatalf("second migration overwrote explicit threshold: %d", long)
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
	if got.Revision != 1 {
		t.Errorf("Revision = %d, want 1 after status mutation", got.Revision)
	}
}

func TestConversationRevisionIsAtomicAndMonotonic(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "rev", Name: "n", Agent: "a"}); err != nil {
		t.Fatal(err)
	}
	for _, turn := range []struct{ role, content string }{{"user", "one"}, {"assistant", "two"}} {
		if err := s.AddConversationTurn("rev", turn.role, turn.content); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetSession("rev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Generation != 1 {
		t.Fatalf("watermark = (%d,%d), want (2,1)", got.Revision, got.Generation)
	}
	deleted, err := s.DeleteLastTurnIfMatches("rev", "assistant", "two")
	if err != nil || !deleted {
		t.Fatalf("DeleteLastTurnIfMatches = %v, %v", deleted, err)
	}
	got, err = s.GetSession("rev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 3 {
		t.Errorf("Revision = %d, want 3 after delete", got.Revision)
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

func TestAgentMessageProvenancePersistsForImmediateAndQueuedTurns(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "target", Name: "target", Agent: "agent"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := s.AddConversationTurnFrom("target", "user", "immediate", "source-id", "ian"); err != nil {
		t.Fatalf("AddConversationTurnFrom: %v", err)
	}
	if err := s.QueuePromptChannelFrom("target", "queued", "", "", "", "", "source-id", "ian", 0, false); err != nil {
		t.Fatalf("QueuePromptChannelFrom: %v", err)
	}
	queued, err := s.DrainQueueGroup("target")
	if err != nil {
		t.Fatalf("DrainQueueGroup: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("len(queued) = %d, want 1", len(queued))
	}
	if queued[0].SourceSessionID != "source-id" || queued[0].SourceName != "ian" {
		t.Errorf("queued provenance = (%q, %q), want (%q, %q)", queued[0].SourceSessionID, queued[0].SourceName, "source-id", "ian")
	}
	if err := s.AddConversationTurnFrom("target", "user", queued[0].Prompt, queued[0].SourceSessionID, queued[0].SourceName); err != nil {
		t.Fatalf("AddConversationTurnFrom queued: %v", err)
	}
	turns, err := s.GetConversation("target")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("len(turns) = %d, want 2", len(turns))
	}
	for _, turn := range turns {
		if turn.SourceSessionID != "source-id" || turn.SourceName != "ian" {
			t.Errorf("turn provenance = (%q, %q), want (%q, %q)", turn.SourceSessionID, turn.SourceName, "source-id", "ian")
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

func TestCommitCompactionHandoffIsAtomic(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "handoff", Name: "handoff", Agent: "claude", AgentSessionID: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitCompactionHandoff("handoff", "new", 200000, "summary"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetSession("handoff")
	if err != nil {
		t.Fatal(err)
	}
	if sess.AgentSessionID != "new" || sess.ContextTokens != 0 || sess.ContextWindow != 200000 || sess.Revision != 3 {
		t.Fatalf("handoff metadata = %+v", sess)
	}
	turns, err := s.GetConversation("handoff")
	if err != nil || len(turns) != 2 || turns[0].Role != "compaction" || turns[1].Role != "compaction_summary" {
		t.Fatalf("handoff turns = %#v, err=%v", turns, err)
	}
}

func TestDrainQueueGroupSeparatesDurableOperationsAndBatchesLegacy(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "queue-groups", Name: "queue-groups", Agent: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePromptChannelFromOperation("queue-groups", "legacy", "", "", "", "", "", "", 0, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePromptChannelFromOperation("queue-groups", "op-a", "", "", "", "", "", "", 0, false, "op-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePromptChannelFromOperation("queue-groups", "op-b", "", "", "", "", "", "", 0, false, "op-b"); err != nil {
		t.Fatal(err)
	}
	group, err := s.DrainQueueGroup("queue-groups")
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 1 || group[0].OperationID != "" {
		t.Fatalf("first group = %#v, want legacy only", group)
	}
	group, err = s.DrainQueueGroup("queue-groups")
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 1 || group[0].OperationID != "op-a" {
		t.Fatalf("second group = %#v, want op-a only", group)
	}
	group, err = s.DrainQueueGroup("queue-groups")
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 1 || group[0].OperationID != "op-b" {
		t.Fatalf("third group = %#v, want op-b only", group)
	}
}
