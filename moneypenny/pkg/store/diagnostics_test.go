package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
)

func TestSessionDiagnosticsAggregatesWithoutPayloads(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"target", "other"} {
		if err := s.CreateSession(&Session{SessionID: id, Name: "same-name", Agent: "copilot"}); err != nil {
			t.Fatal(err)
		}
	}
	payload := strings.Repeat("private-prompt", 10000) + "é\x00tail"
	for _, status := range []string{SchedulePending, ScheduleRunning, ScheduleDone, "legacy"} {
		if _, err := s.db.Exec(`INSERT INTO schedules (session_id, prompt, scheduled_at, status) VALUES (?, ?, ?, ?)`, "target", payload, time.Now(), status); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddConversationTurn("target", "user", payload); err != nil {
		t.Fatal(err)
	}
	if err := s.AddConversationTurn("target", "assistant", "é"); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePrompt("target", payload, "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddConversationTurn("other", "user", payload); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionDiagnostics(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	size := int64(len(payload))
	if got.Status != StateIdle || got.Schedules.PayloadDiagnostics != (envelope.PayloadDiagnostics{Rows: 4, TotalBytes: size * 4, MaxBytes: size}) {
		t.Fatalf("schedules/status: %+v", got)
	}
	if got.Schedules.Pending != 1 || got.Schedules.Running != 1 || got.Schedules.Done != 1 || got.Schedules.Other != 1 {
		t.Fatalf("schedule status counts: %+v", got.Schedules)
	}
	if got.Conversation != (envelope.PayloadDiagnostics{Rows: 2, TotalBytes: size + 2, MaxBytes: size}) ||
		got.Queue != (envelope.PayloadDiagnostics{Rows: 1, TotalBytes: size, MaxBytes: size}) {
		t.Fatalf("payload counts: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || len(encoded) > 1024 || strings.Contains(string(encoded), "private-prompt") {
		t.Fatalf("diagnostics not bounded/content-free: bytes=%d error=%v", len(encoded), err)
	}
	other, err := s.SessionDiagnostics(context.Background(), "other")
	if err != nil || other.Schedules.Rows != 0 || other.Queue.Rows != 0 {
		t.Fatalf("empty aggregates: %+v error=%v", other, err)
	}
	if result, err := s.SessionDiagnostics(context.Background(), "same-name"); err == nil || result != nil {
		t.Fatal("name must not silently resolve a session")
	}
}

func TestSessionDiagnosticsCancellationAndPoolContention(t *testing.T) {
	s := newTestStore(t)
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stats := s.DatabaseDiagnostics()
	if stats.InUse != 1 || stats.MaxOpenConnections != 1 {
		t.Fatalf("pool stats: %+v", stats)
	}
	for _, deadline := range []time.Duration{0, 20 * time.Millisecond} {
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		start := time.Now()
		result, err := s.SessionDiagnostics(ctx, "target")
		cancel()
		if result != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("partial success on cancelled scan: result=%+v error=%v", result, err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("scan waited for a busy database past cancellation")
		}
	}
}
