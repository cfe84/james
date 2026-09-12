package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func createDueSchedule(t *testing.T, s *Store, cron string) *Schedule {
	t.Helper()
	id, err := s.CreateScheduleFull("sess", "check progress", time.Now().Add(-time.Hour), cron, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := s.GetSchedule(id)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func TestScheduleDispatchLifecycle(t *testing.T) {
	for _, busy := range []bool{false, true} {
		for _, cron := range []string{"", "*/10 * * * *"} {
			t.Run(fmt.Sprintf("busy=%t/cron=%s", busy, cron), func(t *testing.T) {
				s := newTestStore(t)
				if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
					t.Fatal(err)
				}
				if busy {
					if err := s.UpdateSessionStatus("sess", StateWorking); err != nil {
						t.Fatal(err)
					}
				}
				sch := createDueSchedule(t, s, cron)
				next := time.Now().Add(time.Hour)
				got, err := s.DispatchSchedule(context.Background(), sch, next)
				if err != nil || !got.Delivered || got.Start == busy {
					t.Fatalf("dispatch=%+v err=%v", got, err)
				}
				got, err = s.DispatchSchedule(context.Background(), sch, next)
				if err != nil || got != (ScheduleDispatch{}) {
					t.Fatalf("stale dispatch=%+v err=%v", got, err)
				}
				schedules, err := s.ListSchedules("sess", "")
				if err != nil || len(schedules) != 1 || schedules[0].ID != sch.ID {
					t.Fatalf("schedules=%v err=%v", schedules, err)
				}
				wantStatus := ScheduleDone
				if cron != "" {
					wantStatus = SchedulePending
					if !schedules[0].ScheduledAt.Equal(next) {
						t.Fatal("recurrence not advanced")
					}
				}
				if schedules[0].Status != wantStatus {
					t.Fatalf("status=%s", schedules[0].Status)
				}
				if busy && cron != "" {
					// Hundreds of ticks while busy must retain just one queued
					// delivery, one schedule, and one visible notification.
					for i := 0; i < 100; i++ {
						if _, err := s.db.Exec(`UPDATE schedules SET scheduled_at = ? WHERE id = ?`, sch.ScheduledAt, sch.ID); err != nil {
							t.Fatal(err)
						}
						got, err = s.DispatchSchedule(context.Background(), sch, next)
						if err != nil || !got.Coalesced || got.Delivered {
							t.Fatalf("coalesce=%+v err=%v", got, err)
						}
					}
				}
				turns, err := s.GetConversation("sess")
				wantTurns := 2
				if busy {
					wantTurns = 1
				}
				if err != nil || len(turns) != wantTurns {
					t.Fatalf("turns=%d err=%v", len(turns), err)
				}
				queue, err := s.DrainQueueGroup("sess")
				if err != nil {
					t.Fatal(err)
				}
				if busy {
					if len(queue) != 1 || queue[0].Source != "scheduled" || queue[0].ReplyChannelID != 42 || !queue[0].MarkReady {
						t.Fatalf("queue=%+v", queue)
					}
				} else if len(queue) != 0 {
					t.Fatalf("unexpected queued delivery: %v", queue)
				}
			})
		}
	}
}

func TestScheduleDispatchRollbackAndRevocation(t *testing.T) {
	for _, action := range []string{"cancel", "edit", "queue-full", "insert-failure", "cancelled-context"} {
		t.Run(action, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateSessionStatus("sess", StateWorking); err != nil {
				t.Fatal(err)
			}
			sch := createDueSchedule(t, s, "@every 1h")
			ctx := context.Background()
			switch action {
			case "cancel":
				if err := s.CancelSchedule(sch.ID); err != nil {
					t.Fatal(err)
				}
			case "edit":
				if err := s.UpdateSchedule(sch.ID, "new prompt", sch.ScheduledAt, sch.CronExpr, 0, false); err != nil {
					t.Fatal(err)
				}
			case "queue-full":
				for i := 0; i < MaxScheduledQueue; i++ {
					if err := s.QueuePrompt("sess", "old", "", "", "", "scheduled"); err != nil {
						t.Fatal(err)
					}
				}
			case "insert-failure":
				if _, err := s.db.Exec(`CREATE TRIGGER fail_queue BEFORE INSERT ON prompt_queue BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := s.DispatchSchedule(ctx, sch, time.Now().Add(time.Hour))
			wantError := action == "insert-failure" || action == "cancelled-context"
			if (err != nil) != wantError || got.Delivered || got.Start {
				t.Fatalf("dispatch=%+v err=%v", got, err)
			}
			if (action == "queue-full") != got.Deferred {
				t.Fatalf("deferred=%v", got.Deferred)
			}
			current, err := s.GetSchedule(sch.ID)
			if err != nil {
				t.Fatal(err)
			}
			if action != "cancel" && (current.Status != SchedulePending || !current.ScheduledAt.Equal(sch.ScheduledAt)) {
				t.Fatalf("occurrence lost: %+v", current)
			}
			turns, err := s.GetConversation("sess")
			if err != nil || len(turns) != 0 {
				t.Fatalf("phantom turns=%v err=%v", turns, err)
			}
		})
	}
}

func TestConcurrentScheduleDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	sch := createDueSchedule(t, s, "@every 1h")
	var wg sync.WaitGroup
	results := make(chan ScheduleDispatch, 16)
	for i := 0; i < 16; i++ {
		db := []*Store{s, other}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := db.DispatchSchedule(context.Background(), sch, time.Now().Add(time.Hour))
			if err != nil {
				t.Error(err)
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	started := 0
	for result := range results {
		if result.Start {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("started=%d", started)
	}
}

func TestScheduleAndQueueBounds(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxActiveSchedules; i++ {
		createDueSchedule(t, s, "")
	}
	if _, err := s.CreateSchedule("sess", "too many", time.Now()); err == nil {
		t.Fatal("schedule limit not enforced")
	}
	due, err := s.DueSchedules(context.Background())
	if err != nil || len(due) != 1 {
		t.Fatalf("due=%d err=%v", len(due), err)
	}
	for i := 0; i < MaxScheduleBatch+2; i++ {
		id := fmt.Sprintf("other-%d", i)
		if err := s.CreateSession(&Session{SessionID: id, Name: id, Agent: "copilot"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateSchedule(id, "check", time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	due, err = s.DueSchedules(context.Background())
	if err != nil || len(due) != MaxScheduleBatch {
		t.Fatalf("batch=%d err=%v", len(due), err)
	}
	for i := 0; i < MaxQueueBatch+3; i++ {
		if err := s.QueuePrompt("sess", fmt.Sprint(i), "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	group, err := s.DrainQueueGroup("sess")
	if err != nil || len(group) != MaxQueueBatch {
		t.Fatalf("group=%d err=%v", len(group), err)
	}
	group, err = s.DrainQueueGroup("sess")
	if err != nil || len(group) != 3 || group[0].Prompt != fmt.Sprint(MaxQueueBatch) {
		t.Fatalf("remainder=%v err=%v", group, err)
	}
	for i := 0; i < 3; i++ {
		if err := s.QueuePrompt("sess", strings.Repeat("x", MaxQueueBatchBytes/2), "", "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	group, err = s.DrainQueueGroup("sess")
	if err != nil || len(group) != 2 {
		t.Fatalf("byte-bound group=%d err=%v", len(group), err)
	}
	group, err = s.DrainQueueGroup("sess")
	if err != nil || len(group) != 1 {
		t.Fatalf("byte-bound remainder=%d err=%v", len(group), err)
	}
	if _, err := s.DrainQueueGroup("sess"); err != nil {
		t.Fatal(err)
	}
	session, err := s.GetSession("sess")
	if err != nil || session.Status != StateIdle {
		t.Fatalf("session=%v err=%v", session, err)
	}
}

func TestIdleQueueRecoveryAndHistoryPruning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePromptChannel("sess", "ready", "", "", "", "scheduled", 42, true); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePromptChannel("sess", "not ready", "", "", "", "scheduled", 42, false); err != nil {
		t.Fatal(err)
	}
	ids, err := s.IdleQueuedSessions(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "sess" {
		t.Fatalf("recovery ids=%v err=%v", ids, err)
	}
	for i := 0; i < 2; i++ {
		claimed, err := s.ClaimIdleSession(ctx, "sess")
		if err != nil || claimed != (i == 0) {
			t.Fatalf("claim %d=%v err=%v", i, claimed, err)
		}
	}
	for _, ready := range []bool{true, false} {
		group, err := s.DrainQueueGroup("sess")
		if err != nil || len(group) != 1 || group[0].MarkReady != ready {
			t.Fatalf("Ready grouping=%+v err=%v", group, err)
		}
	}
	session, err := s.GetSession("sess")
	if err != nil || session.Status != StateWorking {
		t.Fatalf("released session before running drained prompts: %v, %v", session, err)
	}
	if _, err := s.DrainQueueGroup("sess"); err != nil {
		t.Fatal(err)
	}
	session, err = s.GetSession("sess")
	if err != nil || session.Status != StateIdle {
		t.Fatalf("empty drain did not release working session: %v, %v", session, err)
	}
	ids, err = s.IdleQueuedSessions(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty queue recovery ids=%v err=%v", ids, err)
	}
	for i := 0; i < MaxScheduleBatch+2; i++ {
		id, err := s.CreateSchedule("sess", "old", time.Now().Add(-31*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateScheduleStatus(id, ScheduleDone); err != nil {
			t.Fatal(err)
		}
	}
	pending := createDueSchedule(t, s, "@every 1h")
	recent := createDueSchedule(t, s, "")
	if err := s.UpdateScheduleStatus(recent.ID, ScheduleDone); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneScheduleHistory(ctx); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSchedules("sess", "")
	if err != nil || len(all) != 4 {
		t.Fatalf("history pruning rows=%d err=%v", len(all), err)
	}
	for _, id := range []int64{pending.ID, recent.ID} {
		sch, err := s.GetSchedule(id)
		if err != nil || sch == nil {
			t.Fatalf("pruned live/recent schedule %d: %v", id, err)
		}
	}
}

func TestCancelRecurringDelivery(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus("sess", StateWorking); err != nil {
		t.Fatal(err)
	}
	sch := createDueSchedule(t, s, "@every 1h")
	if _, err := s.DispatchSchedule(context.Background(), sch, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.QueuePrompt("sess", "human message", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelSchedule(sch.ID); err != nil {
		t.Fatal(err)
	}
	group, err := s.DrainQueueGroup("sess")
	if err != nil || len(group) != 1 || group[0].Prompt != "human message" {
		t.Fatalf("queue=%+v err=%v", group, err)
	}
}

func TestOfflineBacklogCleanupAndWALCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.db.SetMaxOpenConns(1)
	if _, err := s.db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"affected", "unaffected"} {
		if err := s.CreateSession(&Session{SessionID: id, Name: id, Agent: "copilot"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateSchedule(id, "task", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.AddConversationTurn(id, "user", "preserve history"); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{"scheduled", "", "channel", "callback"} {
			if err := s.QueuePrompt(id, strings.Repeat("p", 8192), "", "", "", source); err != nil {
				t.Fatal(err)
			}
		}
	}
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected nonempty WAL: %v", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`DELETE FROM prompt_queue WHERE session_id = ? AND source = 'scheduled'`,
		`DELETE FROM schedules WHERE session_id = ?`,
	} {
		if _, err := tx.Exec(query, "affected"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var busy, frames, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 || frames != 0 || checkpointed != 0 {
		t.Fatalf("checkpoint=%d|%d|%d", busy, frames, checkpointed)
	}
	info, err = os.Stat(path + "-wal")
	if err != nil || info.Size() != 0 {
		t.Fatalf("WAL not truncated: %v", err)
	}
	var check string
	if err := s.db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity=%s err=%v", check, err)
	}
	for _, id := range []string{"affected", "unaffected"} {
		wantQueue, wantSchedules := 3, 0
		if id == "unaffected" {
			wantQueue, wantSchedules = 4, 1
		}
		queue, err := s.QueueLength(id)
		if err != nil || queue != wantQueue {
			t.Fatalf("%s queue=%d err=%v", id, queue, err)
		}
		schedules, err := s.ListSchedules(id, "")
		if err != nil || len(schedules) != wantSchedules {
			t.Fatalf("%s schedules=%d err=%v", id, len(schedules), err)
		}
		turns, err := s.GetConversation(id)
		if err != nil || len(turns) != 1 {
			t.Fatalf("%s history not preserved: %v", id, err)
		}
	}
}

func TestUpdateScheduleInPlace(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "n", Agent: "copilot"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	id, err := s.CreateScheduleFull("sess", "original prompt", at, "0 9 * * *", 7, true)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}

	newAt := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	if err := s.UpdateSchedule(id, "edited prompt", newAt, "0 13 * * 1-5", 42, false); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}

	got, err := s.GetSchedule(id)
	if err != nil || got == nil {
		t.Fatalf("GetSchedule: %v (nil=%v)", err, got == nil)
	}
	if got.ID != id {
		t.Errorf("ID changed: got %d want %d", got.ID, id)
	}
	if got.Prompt != "edited prompt" {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "edited prompt")
	}
	if !got.ScheduledAt.Equal(newAt) {
		t.Errorf("ScheduledAt = %v, want %v", got.ScheduledAt, newAt)
	}
	if got.CronExpr != "0 13 * * 1-5" {
		t.Errorf("CronExpr = %q, want %q", got.CronExpr, "0 13 * * 1-5")
	}
	if got.ReplyChannelID != 42 {
		t.Errorf("ReplyChannelID = %d, want 42", got.ReplyChannelID)
	}
	if got.MarkReady {
		t.Error("MarkReady = true, want false after update")
	}
	if got.Status != SchedulePending {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestUpdateScheduleClearsCronAndChannel(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "n", Agent: "copilot"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	id, err := s.CreateScheduleFull("sess", "p", at, "0 9 * * *", 5, false)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}
	if err := s.UpdateSchedule(id, "p", at, "", 0, false); err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	got, _ := s.GetSchedule(id)
	if got.CronExpr != "" {
		t.Errorf("CronExpr not cleared: %q", got.CronExpr)
	}
	if got.ReplyChannelID != 0 {
		t.Errorf("ReplyChannelID not cleared: %d", got.ReplyChannelID)
	}
}

func TestUpdateScheduleRejectsNonPending(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "n", Agent: "copilot"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	at := time.Now().Add(time.Hour).UTC()
	id, err := s.CreateScheduleFull("sess", "p", at, "", 0, false)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}
	if err := s.UpdateScheduleStatus(id, ScheduleDone); err != nil {
		t.Fatalf("UpdateScheduleStatus: %v", err)
	}
	if err := s.UpdateSchedule(id, "x", at, "", 0, false); err == nil {
		t.Fatal("expected error updating a non-pending schedule, got nil")
	}
}

func TestUpdateScheduleMissing(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpdateSchedule(9999, "x", time.Now(), "", 0, false); err == nil {
		t.Fatal("expected error updating a missing schedule, got nil")
	}
}
