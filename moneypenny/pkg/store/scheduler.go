package store

import (
	"context"
	"fmt"
	"time"
)

const (
	MaxScheduleBatch   = 32
	MaxScheduledQueue  = 32
	MaxActiveSchedules = 100
)

// ScheduleDispatch describes a committed delivery. Start means the caller owns
// the idle-to-working transition and must run the prompt.
type ScheduleDispatch struct {
	Start     bool
	Delivered bool
	Coalesced bool
	Deferred  bool
}

// DispatchSchedule atomically consumes the due occurrence and either claims an
// idle session or queues a delivery. Recurrences keep their ID and skip missed
// intervals. A stale snapshot (including edits/cancellation) has no effect.
func (s *Store) DispatchSchedule(ctx context.Context, sch *Schedule, next time.Time) (ScheduleDispatch, error) {
	result := ScheduleDispatch{}
	now := time.Now()
	if sch.CronExpr != "" && !next.After(now) {
		return result, fmt.Errorf("schedule %d next occurrence must be in the future", sch.ID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	status := ScheduleDone
	at := sch.ScheduledAt
	if sch.CronExpr != "" {
		status, at = SchedulePending, next
	}
	claim, err := tx.ExecContext(ctx, `UPDATE schedules SET status = ?, scheduled_at = ?
		WHERE id = ? AND status = ? AND scheduled_at = ? AND scheduled_at <= ?
		AND prompt = ? AND cron_expr = ? AND reply_channel_id = ? AND mark_ready = ?`,
		status, at.UTC(), sch.ID, SchedulePending, sch.ScheduledAt.UTC(), now.UTC(),
		sch.Prompt, sch.CronExpr, sch.ReplyChannelID, boolToInt(sch.MarkReady))
	if err != nil {
		return result, fmt.Errorf("claim schedule: %w", err)
	}
	n, err := claim.RowsAffected()
	if err != nil || n == 0 {
		return result, err
	}

	// Existing recurring delivery wins even if the agent just became idle.
	// The idle-queue recovery pass will pick it up without creating a duplicate.
	var queued int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM prompt_queue WHERE schedule_id = ? AND schedule_id != 0`, sch.ID).Scan(&queued); err != nil {
		return result, err
	}
	if sch.CronExpr != "" && queued > 0 {
		result.Coalesced = true
		return result, tx.Commit()
	}
	claim, err = tx.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ?
		WHERE session_id = ? AND status = ?
		AND NOT EXISTS (SELECT 1 FROM prompt_queue WHERE session_id = ?)`,
		StateWorking, now.UTC(), sch.SessionID, StateIdle, sch.SessionID)
	if err != nil {
		return result, err
	}
	n, err = claim.RowsAffected()
	if err != nil {
		return result, err
	}
	result.Start = n > 0
	if !result.Start {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM
			(SELECT 1 FROM prompt_queue WHERE session_id = ? AND source = 'scheduled' LIMIT ?)`,
			sch.SessionID, MaxScheduledQueue).Scan(&queued); err != nil {
			return result, err
		}
		if queued >= MaxScheduledQueue {
			// Roll back the occurrence too: no lost one-shot or hidden success.
			return ScheduleDispatch{Deferred: true}, nil
		}
		scheduleID := int64(0)
		if sch.CronExpr != "" {
			scheduleID = sch.ID
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO prompt_queue
			(session_id, prompt, source, reply_channel_id, mark_ready, schedule_id)
			VALUES (?, ?, 'scheduled', ?, ?, ?)`,
			sch.SessionID, sch.Prompt, sch.ReplyChannelID, boolToInt(sch.MarkReady), scheduleID)
		if err != nil {
			return result, fmt.Errorf("queue scheduled prompt: %w", err)
		}
	}
	label := "Scheduled task"
	if sch.CronExpr != "" {
		label = fmt.Sprintf("Recurring task (%s)", sch.CronExpr)
	}
	notice := fmt.Sprintf("[%s triggered at %s]", label, now.Local().Format("Jan 2, 3:04 PM"))
	turn, err := tx.ExecContext(ctx, `INSERT INTO conversation_turns (session_id, role, content) VALUES (?, 'system', ?)`, sch.SessionID, notice)
	if err != nil {
		return result, err
	}
	noticeID, err := turn.LastInsertId()
	if err != nil {
		return result, err
	}
	var promptID int64
	if result.Start {
		turn, err := tx.ExecContext(ctx, `INSERT INTO conversation_turns (session_id, role, content) VALUES (?, 'scheduled', ?)`, sch.SessionID, sch.Prompt)
		if err != nil {
			return result, err
		}
		promptID, err = turn.LastInsertId()
		if err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	s.notifyConversationTurn(sch.SessionID, "system", notice, "", "", noticeID)
	if result.Start {
		s.notifyConversationTurn(sch.SessionID, "scheduled", sch.Prompt, "", "", promptID)
	}
	result.Delivered = true
	return result, nil
}

// IdleQueuedSessions finds bounded recovery work after restart or a failed drain.
func (s *Store) IdleQueuedSessions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id FROM sessions
		WHERE status = ? AND EXISTS (SELECT 1 FROM prompt_queue WHERE prompt_queue.session_id = sessions.session_id)
		ORDER BY updated_at LIMIT ?`, StateIdle, MaxScheduleBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimIdleSession elects one caller to resume queued work.
func (s *Store) ClaimIdleSession(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET status = ?, updated_at = ? WHERE session_id = ? AND status = ?`,
		StateWorking, time.Now().UTC(), id, StateIdle)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PruneScheduleHistory bounds maintenance transactions; recurring schedules no
// longer generate history rows. Keep one-shot history for thirty days.
func (s *Store) PruneScheduleHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id IN
		(SELECT id FROM schedules WHERE status = ? AND scheduled_at < ? ORDER BY scheduled_at LIMIT ?)`,
		ScheduleDone, time.Now().UTC().Add(-30*24*time.Hour), MaxScheduleBatch)
	return err
}
