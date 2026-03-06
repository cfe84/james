package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Session states
const (
	StateIdle    = "idle"
	StateWorking = "working"
)

// Session represents a stored session.
type Session struct {
	SessionID    string
	Name         string
	Agent        string
	SystemPrompt string
	Yolo         bool
	Path         string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ConversationTurn represents a stored prompt or response.
type ConversationTurn struct {
	ID        int64
	SessionID string
	Role      string // "user" or "assistant"
	Content   string
	CreatedAt time.Time
}

// Schedule states
const (
	SchedulePending = "pending"
	ScheduleRunning = "running"
	ScheduleDone    = "done"
)

// Schedule represents a scheduled prompt for a session.
type Schedule struct {
	ID          int64
	SessionID   string
	Prompt      string
	ScheduledAt time.Time
	Status      string
	CronExpr    string // cron expression for recurring schedules (empty = one-shot)
	CreatedAt   time.Time
}

// Store manages the SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at the given path and runs migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode and foreign keys.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Run migrations.
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    agent TEXT NOT NULL,
    system_prompt TEXT NOT NULL DEFAULT '',
    yolo INTEGER NOT NULL DEFAULT 0,
    path TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'idle',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS conversation_turns (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conversation_session ON conversation_turns(session_id);

CREATE TABLE IF NOT EXISTS prompt_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    prompt TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_prompt_queue_session ON prompt_queue(session_id);

CREATE TABLE IF NOT EXISTS schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    prompt TEXT NOT NULL,
    scheduled_at DATETIME NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    cron_expr TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_schedules_session ON schedules(session_id);
CREATE INDEX IF NOT EXISTS idx_schedules_pending ON schedules(status, scheduled_at);
`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: add cron_expr column to schedules if missing (for existing DBs).
	db.Exec(`ALTER TABLE schedules ADD COLUMN cron_expr TEXT NOT NULL DEFAULT ''`)

	return nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// CreateSession inserts a new session. Returns error if session_id already exists.
func (s *Store) CreateSession(sess *Session) error {
	now := time.Now().UTC()
	sess.Status = StateIdle
	sess.CreatedAt = now
	sess.UpdatedAt = now

	yolo := 0
	if sess.Yolo {
		yolo = 1
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (session_id, name, agent, system_prompt, yolo, path, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.SessionID, sess.Name, sess.Agent, sess.SystemPrompt, yolo, sess.Path, sess.Status, now, now,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID. Returns nil, nil if not found.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT session_id, name, agent, system_prompt, yolo, path, status, created_at, updated_at
		 FROM sessions WHERE session_id = ?`, sessionID,
	)

	sess := &Session{}
	var yolo int
	err := row.Scan(
		&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt,
		&yolo, &sess.Path, &sess.Status, &sess.CreatedAt, &sess.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess.Yolo = yolo != 0
	return sess, nil
}

// ListSessions returns all sessions.
func (s *Store) ListSessions() ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT session_id, name, agent, system_prompt, yolo, path, status, created_at, updated_at
		 FROM sessions ORDER BY created_at`,
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		sess := &Session{}
		var yolo int
		if err := rows.Scan(
			&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt,
			&yolo, &sess.Path, &sess.Status, &sess.CreatedAt, &sess.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.Yolo = yolo != 0
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// UpdateSessionFields updates specific fields of a session.
func (s *Store) UpdateSessionFields(sessionID string, name, systemPrompt, path *string, yolo *bool) error {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return err
	}
	if sess == nil {
		return fmt.Errorf("session %q not found", sessionID)
	}

	if name != nil {
		sess.Name = *name
	}
	if systemPrompt != nil {
		sess.SystemPrompt = *systemPrompt
	}
	if path != nil {
		sess.Path = *path
	}
	if yolo != nil {
		sess.Yolo = *yolo
	}

	now := time.Now().UTC()
	yoloInt := 0
	if sess.Yolo {
		yoloInt = 1
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET name = ?, system_prompt = ?, yolo = ?, path = ?, updated_at = ? WHERE session_id = ?`,
		sess.Name, sess.SystemPrompt, yoloInt, sess.Path, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

// UpdateSessionStatus updates the status of a session.
func (s *Store) UpdateSessionStatus(sessionID string, status string) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE sessions SET status = ?, updated_at = ? WHERE session_id = ?`,
		status, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("update session status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return nil
}

// DeleteSession deletes a session and its conversation history.
func (s *Store) DeleteSession(sessionID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM conversation_turns WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete conversation turns: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM sessions WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	return tx.Commit()
}

// AddConversationTurn adds a turn to the conversation history.
func (s *Store) AddConversationTurn(sessionID string, role string, content string) error {
	_, err := s.db.Exec(
		`INSERT INTO conversation_turns (session_id, role, content) VALUES (?, ?, ?)`,
		sessionID, role, content,
	)
	if err != nil {
		return fmt.Errorf("add conversation turn: %w", err)
	}
	return nil
}

// SessionTimestamps holds the first and last conversation turn timestamps.
type SessionTimestamps struct {
	FirstTurn time.Time
	LastTurn  time.Time
}

// GetSessionTimestamps returns the first and last conversation turn timestamps for a session.
func (s *Store) GetSessionTimestamps(sessionID string) (*SessionTimestamps, error) {
	row := s.db.QueryRow(
		`SELECT MIN(created_at), MAX(created_at) FROM conversation_turns WHERE session_id = ?`, sessionID,
	)
	var minT, maxT sql.NullTime
	if err := row.Scan(&minT, &maxT); err != nil {
		return nil, fmt.Errorf("get session timestamps: %w", err)
	}
	if !minT.Valid {
		return nil, nil
	}
	return &SessionTimestamps{FirstTurn: minT.Time, LastTurn: maxT.Time}, nil
}

// GetConversation returns all turns for a session, ordered by creation time.
func (s *Store) GetConversation(sessionID string) ([]*ConversationTurn, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, role, content, created_at
		 FROM conversation_turns WHERE session_id = ? ORDER BY created_at, id`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	defer rows.Close()

	var turns []*ConversationTurn
	for rows.Next() {
		t := &ConversationTurn{}
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// GetConversationCount returns the total number of turns for a session.
func (s *Store) GetConversationCount(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM conversation_turns WHERE session_id = ?`, sessionID,
	).Scan(&count)
	return count, err
}

// GetConversationPaginated returns turns for a session with pagination.
// It returns the last `limit` turns, offset by `offset` from the end.
// For example, limit=10, offset=0 returns the 10 most recent turns.
// limit=10, offset=10 returns turns 11-20 from the end.
func (s *Store) GetConversationPaginated(sessionID string, limit, offset int) ([]*ConversationTurn, error) {
	// We want rows ordered chronologically, but paginated from the end.
	// Use a subquery to get the tail, then re-order.
	rows, err := s.db.Query(
		`SELECT id, session_id, role, content, created_at FROM (
			SELECT id, session_id, role, content, created_at
			FROM conversation_turns WHERE session_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ? OFFSET ?
		) sub ORDER BY created_at, id`, sessionID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation paginated: %w", err)
	}
	defer rows.Close()

	var turns []*ConversationTurn
	for rows.Next() {
		t := &ConversationTurn{}
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// QueuePrompt adds a prompt to the queue for a session.
func (s *Store) QueuePrompt(sessionID, prompt string) error {
	_, err := s.db.Exec(
		`INSERT INTO prompt_queue (session_id, prompt) VALUES (?, ?)`,
		sessionID, prompt,
	)
	if err != nil {
		return fmt.Errorf("queue prompt: %w", err)
	}
	return nil
}

// DrainQueue removes and returns all queued prompts for a session, ordered by creation time.
func (s *Store) DrainQueue(sessionID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT id, prompt FROM prompt_queue WHERE session_id = ? ORDER BY created_at, id`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("drain queue: %w", err)
	}
	defer rows.Close()

	var ids []int64
	var prompts []string
	for rows.Next() {
		var id int64
		var prompt string
		if err := rows.Scan(&id, &prompt); err != nil {
			return nil, fmt.Errorf("scan queued prompt: %w", err)
		}
		ids = append(ids, id)
		prompts = append(prompts, prompt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Delete drained prompts.
	for _, id := range ids {
		s.db.Exec(`DELETE FROM prompt_queue WHERE id = ?`, id)
	}

	return prompts, nil
}

// QueueLength returns the number of queued prompts for a session.
func (s *Store) QueueLength(sessionID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM prompt_queue WHERE session_id = ?`, sessionID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("queue length: %w", err)
	}
	return count, nil
}

// CreateSchedule adds a scheduled prompt for a session.
func (s *Store) CreateSchedule(sessionID, prompt string, scheduledAt time.Time) (int64, error) {
	return s.CreateScheduleWithCron(sessionID, prompt, scheduledAt, "")
}

// CreateScheduleWithCron adds a scheduled prompt with an optional cron expression for recurrence.
func (s *Store) CreateScheduleWithCron(sessionID, prompt string, scheduledAt time.Time, cronExpr string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO schedules (session_id, prompt, scheduled_at, status, cron_expr) VALUES (?, ?, ?, ?, ?)`,
		sessionID, prompt, scheduledAt.UTC(), SchedulePending, cronExpr,
	)
	if err != nil {
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	return res.LastInsertId()
}

// GetSchedule retrieves a schedule by ID.
func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, created_at FROM schedules WHERE id = ?`, id,
	)
	sch := &Schedule{}
	err := row.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get schedule: %w", err)
	}
	return sch, nil
}

// ListSchedules returns schedules for a session, optionally filtered by status.
func (s *Store) ListSchedules(sessionID string, statusFilter string) ([]*Schedule, error) {
	var rows *sql.Rows
	var err error
	if statusFilter != "" {
		rows, err = s.db.Query(
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, created_at
			 FROM schedules WHERE session_id = ? AND status = ? ORDER BY scheduled_at`, sessionID, statusFilter,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, created_at
			 FROM schedules WHERE session_id = ? ORDER BY scheduled_at`, sessionID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*Schedule
	for rows.Next() {
		sch := &Schedule{}
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		schedules = append(schedules, sch)
	}
	return schedules, rows.Err()
}

// DueSchedules returns all pending schedules that are due (scheduled_at <= now).
func (s *Store) DueSchedules() ([]*Schedule, error) {
	now := time.Now().UTC()
	rows, err := s.db.Query(
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, created_at
		 FROM schedules WHERE status = ? AND scheduled_at <= ? ORDER BY scheduled_at`,
		SchedulePending, now,
	)
	if err != nil {
		return nil, fmt.Errorf("due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*Schedule
	for rows.Next() {
		sch := &Schedule{}
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		schedules = append(schedules, sch)
	}
	return schedules, rows.Err()
}

// UpdateScheduleStatus updates the status of a schedule.
func (s *Store) UpdateScheduleStatus(id int64, status string) error {
	res, err := s.db.Exec(`UPDATE schedules SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("update schedule status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schedule %d not found", id)
	}
	return nil
}

// CancelSchedule cancels a pending schedule. Returns error if not pending.
func (s *Store) CancelSchedule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM schedules WHERE id = ? AND status = ?`, id, SchedulePending)
	if err != nil {
		return fmt.Errorf("cancel schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schedule %d not found or not pending", id)
	}
	return nil
}
