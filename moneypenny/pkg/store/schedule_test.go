package store

import (
	"testing"
	"time"
)

func TestUpdateScheduleInPlace(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession(&Session{SessionID: "sess", Name: "n", Agent: "copilot"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	at := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	id, err := s.CreateScheduleFull("sess", "original prompt", at, "0 9 * * *", 7)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}

	newAt := time.Now().Add(5 * time.Hour).UTC().Truncate(time.Second)
	if err := s.UpdateSchedule(id, "edited prompt", newAt, "0 13 * * 1-5", 42); err != nil {
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
	id, err := s.CreateScheduleFull("sess", "p", at, "0 9 * * *", 5)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}
	if err := s.UpdateSchedule(id, "p", at, "", 0); err != nil {
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
	id, err := s.CreateScheduleFull("sess", "p", at, "", 0)
	if err != nil {
		t.Fatalf("CreateScheduleFull: %v", err)
	}
	if err := s.UpdateScheduleStatus(id, ScheduleDone); err != nil {
		t.Fatalf("UpdateScheduleStatus: %v", err)
	}
	if err := s.UpdateSchedule(id, "x", at, "", 0); err == nil {
		t.Fatal("expected error updating a non-pending schedule, got nil")
	}
}

func TestUpdateScheduleMissing(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpdateSchedule(9999, "x", time.Now(), "", 0); err == nil {
		t.Fatal("expected error updating a missing schedule, got nil")
	}
}
