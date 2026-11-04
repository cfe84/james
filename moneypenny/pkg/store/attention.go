package store

import (
	"encoding/json"
	"fmt"
	"time"

	"james/moneypenny/pkg/envelope"
)

// SetSessionReport updates a summary/request atomically; identical requests do
// not increment attention revision or generate another delivery.
func (s *Store) SetSessionReport(sessionID string, update envelope.AttentionUpdate, source string) (envelope.SessionReport, bool, error) {
	var report envelope.SessionReport
	if err := update.Validate(); err != nil {
		return report, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, false, err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRow("SELECT report FROM sessions WHERE session_id=?", sessionID).Scan(&raw); err != nil {
		return report, false, fmt.Errorf("read session report: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return report, false, fmt.Errorf("decode session report: %w", err)
	}
	revision := int64(0)
	if report.Attention != nil {
		revision = report.Attention.Revision
	}
	if update.Revision != nil && *update.Revision != revision {
		return report, false, fmt.Errorf("attention changed; refresh before updating (current revision %d)", revision)
	}
	attentionChanged := false
	if update.Kind != nil && ((report.Attention == nil && *update.Kind != "") ||
		(report.Attention != nil && (report.Attention.Kind != *update.Kind || report.Attention.Reason != update.Reason))) {
		report.Attention = &envelope.SessionAttention{
			Kind: *update.Kind, Reason: update.Reason, Revision: revision + 1,
			Source: source, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		attentionChanged = true
	}
	if update.Summary != nil {
		report.Summary = *update.Summary
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return report, false, err
	}
	if string(encoded) == raw {
		return report, false, nil
	}
	if _, err := tx.Exec("UPDATE sessions SET report=?, revision=revision+1, updated_at=? WHERE session_id=?",
		string(encoded), time.Now().UTC(), sessionID); err != nil {
		return report, false, err
	}
	var watermark SessionWatermark
	if err := tx.QueryRow("SELECT revision,generation FROM sessions WHERE session_id=?", sessionID).Scan(&watermark.Revision, &watermark.Generation); err != nil {
		return report, false, err
	}
	if err := tx.Commit(); err != nil {
		return report, false, err
	}
	if s.notifyWriter != nil {
		_ = s.notifyWriter.SendAsync(envelope.EventSessionAttention, sessionID, struct {
			envelope.SessionReport
			Revision   int64 `json:"revision"`
			Generation int64 `json:"generation"`
		}{report, watermark.Revision, watermark.Generation})
	}
	return report, attentionChanged, nil
}
