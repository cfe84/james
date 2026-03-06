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
`
	_, err := db.Exec(schema)
	return err
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
