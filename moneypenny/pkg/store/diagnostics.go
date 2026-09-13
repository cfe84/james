package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"james/moneypenny/pkg/envelope"
)

const diagnosticsScanTimeout = 2 * time.Second

// DatabaseDiagnostics reads pool counters without acquiring a SQL connection.
func (s *Store) DatabaseDiagnostics() envelope.DatabaseDiagnostics {
	stats := s.db.Stats()
	return envelope.DatabaseDiagnostics{
		MaxOpenConnections: stats.MaxOpenConnections,
		OpenConnections:    stats.OpenConnections, InUse: stats.InUse, Idle: stats.Idle,
		WaitCount: stats.WaitCount, WaitDurationNS: int64(stats.WaitDuration),
	}
}

// SessionDiagnostics performs fixed indexed aggregates, never loading payloads
// into Go. Counts are best-effort snapshots across queries, not a transaction.
func (s *Store) SessionDiagnostics(ctx context.Context, sessionID string) (*envelope.SessionDiagnostics, error) {
	ctx, cancel := context.WithTimeout(ctx, diagnosticsScanTimeout)
	defer cancel()
	result := &envelope.SessionDiagnostics{SessionID: sessionID}
	err := s.db.QueryRowContext(ctx, `SELECT substr(status, 1, 64) FROM sessions WHERE session_id = ?`, sessionID).Scan(&result.Status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("session status: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*),
		coalesce(sum(length(CAST(prompt AS BLOB))), 0), coalesce(max(length(CAST(prompt AS BLOB))), 0),
		coalesce(sum(status = 'pending'), 0), coalesce(sum(status = 'running'), 0),
		coalesce(sum(status = 'done'), 0),
		coalesce(sum(status NOT IN ('pending', 'running', 'done')), 0)
		FROM schedules WHERE session_id = ?`, sessionID).Scan(
		&result.Schedules.Rows, &result.Schedules.TotalBytes, &result.Schedules.MaxBytes,
		&result.Schedules.Pending, &result.Schedules.Running, &result.Schedules.Done, &result.Schedules.Other)
	if err != nil {
		return nil, fmt.Errorf("schedules: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*),
		coalesce(sum(length(CAST(content AS BLOB))), 0), coalesce(max(length(CAST(content AS BLOB))), 0)
		FROM conversation_turns WHERE session_id = ?`, sessionID).Scan(
		&result.Conversation.Rows, &result.Conversation.TotalBytes, &result.Conversation.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("conversation: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*),
		coalesce(sum(length(CAST(prompt AS BLOB))), 0), coalesce(max(length(CAST(prompt AS BLOB))), 0)
		FROM prompt_queue WHERE session_id = ?`, sessionID).Scan(
		&result.Queue.Rows, &result.Queue.TotalBytes, &result.Queue.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("queue: %w", err)
	}
	return result, nil
}
