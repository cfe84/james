package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"james/moneypenny/pkg/envelope"
)

func (s *Store) SavePonsonby(p envelope.PonsonbyStore) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ponsonby_stores(id,definition) VALUES(?,?)
		ON CONFLICT(id) DO UPDATE SET definition=excluded.definition`, p.ID, string(raw))
	return err
}

func (s *Store) GetPonsonby(id string) (*envelope.PonsonbyStore, error) {
	var raw string
	err := s.db.QueryRow("SELECT definition FROM ponsonby_stores WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out envelope.PonsonbyStore
	err = json.Unmarshal([]byte(raw), &out)
	return &out, err
}

func (s *Store) SubmitPonsonby(job envelope.PonsonbyJob) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old envelope.PonsonbyJob
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(job.Body)))
	var oldDigest string
	err = tx.QueryRow("SELECT source_id,store_id,kind,digest FROM ponsonby_jobs WHERE id=?", job.ID).Scan(&old.SourceID, &old.StoreID, &old.Kind, &oldDigest)
	if err == nil {
		if old.SourceID != job.SourceID || old.StoreID != job.StoreID || old.Kind != job.Kind || digest != oldDigest {
			return fmt.Errorf("Ponsonby request ID conflict")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec("DELETE FROM ponsonby_jobs WHERE status IN ('done','failed') AND updated_at<datetime('now','-1 day')"); err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRow("SELECT count(*) FROM ponsonby_jobs WHERE status IN ('pending','running')").Scan(&pending); err != nil {
		return err
	}
	if pending >= 64 {
		return fmt.Errorf("Ponsonby queue full (64 outstanding requests); retry after processing")
	}
	// Keep a bounded result history without letting completed requests block
	// new work for the entire retention period.
	if _, err := tx.Exec(`DELETE FROM ponsonby_jobs WHERE id IN (
		SELECT id FROM ponsonby_jobs WHERE status IN ('done','failed')
		ORDER BY updated_at,id LIMIT max(0,(SELECT count(*) FROM ponsonby_jobs)-63))`); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO ponsonby_jobs(id,store_id,source_id,kind,body,digest) VALUES(?,?,?,?,?,?)`,
		job.ID, job.StoreID, job.SourceID, job.Kind, job.Body, digest)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PonsonbyJob(id, sourceID string) (*envelope.PonsonbyJob, error) {
	var j envelope.PonsonbyJob
	err := s.db.QueryRow(`SELECT id,store_id,source_id,kind,status,answer,error FROM ponsonby_jobs WHERE id=? AND source_id=?`, id, sourceID).
		Scan(&j.ID, &j.StoreID, &j.SourceID, &j.Kind, &j.Status, &j.Answer, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &j, err
}

func (s *Store) NextPonsonbyJob() (*envelope.PonsonbyJob, error) {
	var j envelope.PonsonbyJob
	err := s.db.QueryRow(`SELECT id,store_id,source_id,kind,body FROM ponsonby_jobs
		WHERE status='pending' ORDER BY updated_at,id LIMIT 1`).Scan(&j.ID, &j.StoreID, &j.SourceID, &j.Kind, &j.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec("UPDATE ponsonby_jobs SET status='running' WHERE id=? AND status='pending'", j.ID)
	return &j, err
}

func (s *Store) FinishPonsonbyJob(id, answer, failure string) error {
	status := "done"
	if failure != "" {
		status = "failed"
	}
	_, err := s.db.Exec(`UPDATE ponsonby_jobs SET status=?,answer=?,error=?,body=CASE WHEN ?='done' THEN '' ELSE body END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, answer, failure, status, id)
	return err
}

func (s *Store) RecoverPonsonbyJobs() error {
	_, err := s.db.Exec("UPDATE ponsonby_jobs SET status='pending' WHERE status='running'")
	return err
}

func (s *Store) PonsonbyRuns() ([]string, error) {
	rows, err := s.db.Query("SELECT session_id FROM ponsonby_runs")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) TrackPonsonbyRun(id string, add bool) error {
	query := "DELETE FROM ponsonby_runs WHERE session_id=?"
	if add {
		query = "INSERT INTO ponsonby_runs(session_id) VALUES(?)"
	}
	_, err := s.db.Exec(query, id)
	return err
}

func (s *Store) RetryPonsonby(id, storeID string) error {
	res, err := s.db.Exec("UPDATE ponsonby_jobs SET status='pending',error='',updated_at=CURRENT_TIMESTAMP WHERE id=? AND store_id=? AND status='failed'", id, storeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("failed Ponsonby request not found")
	}
	return nil
}

func (s *Store) PonsonbyJobs(storeID string) ([]envelope.PonsonbyJob, error) {
	rows, err := s.db.Query("SELECT id,source_id,kind,status,error FROM ponsonby_jobs WHERE store_id=? ORDER BY updated_at DESC LIMIT 64", storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []envelope.PonsonbyJob{}
	for rows.Next() {
		var j envelope.PonsonbyJob
		j.StoreID = storeID
		if err := rows.Scan(&j.ID, &j.SourceID, &j.Kind, &j.Status, &j.Error); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) SetPonsonbySources(id string, attachments []envelope.Ponsonby) error {
	raw, err := json.Marshal(attachments)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ponsonby_sources(session_id,attachments) VALUES(?,?)
		ON CONFLICT(session_id) DO UPDATE SET attachments=excluded.attachments`, id, string(raw))
	return err
}

func (s *Store) PonsonbySources(id string) ([]envelope.Ponsonby, error) {
	var raw string
	err := s.db.QueryRow("SELECT attachments FROM ponsonby_sources WHERE session_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []envelope.Ponsonby{}, nil
	}
	if err != nil {
		return nil, err
	}
	var attachments []envelope.Ponsonby
	err = json.Unmarshal([]byte(raw), &attachments)
	return attachments, err
}

func (s *Store) UnpublishedPonsonbyTurns() ([]*ConversationTurn, error) {
	rows, err := s.db.Query(`SELECT c.id,c.session_id,c.role,c.content,c.created_at FROM conversation_turns c
		JOIN ponsonby_sources p ON p.session_id=c.session_id
		WHERE c.id>p.published_turn AND p.attachments!='[]' AND c.role IN ('user','assistant','scheduled','callback')
		ORDER BY c.id LIMIT 16`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConversationTurn{}
	for rows.Next() {
		var t ConversationTurn
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (s *Store) MarkPonsonbyPublished(id string, turn int64) error {
	_, err := s.db.Exec("UPDATE ponsonby_sources SET published_turn=max(published_turn,?) WHERE session_id=?", turn, id)
	return err
}

func (s *Store) PrunePonsonbyJobs() error {
	_, err := s.db.Exec("DELETE FROM ponsonby_jobs WHERE status IN ('done','failed') AND updated_at<datetime('now','-1 day')")
	return err
}

func (s *Store) DeletePonsonby(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM ponsonby_jobs WHERE store_id=? AND status IN ('done','failed')", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM ponsonby_stores WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
