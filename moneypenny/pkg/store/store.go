package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"james/moneypenny/pkg/envelope"
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
	Model        string
	Effort       string
	// ContextTier selects copilot's context-window tier (e.g. "long_context"
	// for the 1M window). Copilot-only; empty means the default tier.
	ContextTier string
	Yolo        bool
	Path        string
	Environment string
	Status      string
	Memory      string
	// AgentSessionID is the session id handed to the underlying agent CLI
	// (claude/copilot) via --session-id/--resume. It is decoupled from
	// SessionID so custom compaction can substitute a fresh underlying agent
	// session while keeping the stable James session. Defaults to SessionID.
	AgentSessionID string
	// CompactionMode is "agent" (rely on the agent's built-in compaction) or
	// "custom" (James-managed distillation/summary/substitution).
	CompactionMode string
	// ContextTokens is the last measured/estimated underlying-context size and
	// ContextWindow the model's max context. Used to trigger custom compaction
	// at a threshold and to surface usage in clients.
	ContextTokens int
	ContextWindow int
	// OpenCodeCost is the cumulative provider-reported USD cost for this
	// OpenCode session. It remains zero for other agents.
	OpenCodeCost float64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Compaction modes.
const (
	CompactionAgent  = "agent"
	CompactionCustom = "custom"
)

// ConversationTurn represents a stored prompt or response.
type ConversationTurn struct {
	ID              int64
	SessionID       string
	Role            string // "user" or "assistant"
	Content         string
	SourceSessionID string
	SourceName      string
	CreatedAt       time.Time
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
	// ReplyChannelID routes this scheduled prompt's output to an external
	// communication channel (channels.id). 0 = no channel routing.
	ReplyChannelID int64
	CreatedAt      time.Time
}

// Store manages the SQLite database.
type Store struct {
	db           *sql.DB
	notifyWriter *envelope.NotificationWriter
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

// SetNotificationWriter sets the notification writer for sending real-time events.
func (s *Store) SetNotificationWriter(nw *envelope.NotificationWriter) {
	s.notifyWriter = nw
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
    source_session_id TEXT NOT NULL DEFAULT '',
    source_name TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conversation_session ON conversation_turns(session_id);

CREATE TABLE IF NOT EXISTS prompt_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    prompt TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_session_id TEXT NOT NULL DEFAULT '',
    source_name TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS memory_nodes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(session_id, path)
);

CREATE INDEX IF NOT EXISTS idx_memory_nodes_session ON memory_nodes(session_id);

CREATE TABLE IF NOT EXISTS channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    target_id TEXT NOT NULL,
    target_label TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    mention TEXT NOT NULL DEFAULT '',
    allow_anyone INTEGER NOT NULL DEFAULT 0,
    last_seen_id TEXT NOT NULL DEFAULT '',
    last_seen_ts TEXT NOT NULL DEFAULT '',
    last_activity_at DATETIME,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_channels_session ON channels(session_id);
CREATE INDEX IF NOT EXISTS idx_channels_enabled ON channels(enabled);

CREATE TABLE IF NOT EXISTS channel_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    sent_msg_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_channel_outbox_pending ON channel_outbox(status);
`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: add cron_expr column to schedules if missing (for existing DBs).
	db.Exec(`ALTER TABLE schedules ADD COLUMN cron_expr TEXT NOT NULL DEFAULT ''`)

	// Migration: add model column to sessions if missing.
	db.Exec(`ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT ''`)

	// Migration: add effort column to sessions if missing.
	db.Exec(`ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT ''`)

	// Migration: add context_tier column to sessions if missing. Selects
	// copilot's context-window tier (e.g. "long_context"); empty = default.
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_tier TEXT NOT NULL DEFAULT ''`)

	// Migration: add memory column to sessions if missing.
	db.Exec(`ALTER TABLE sessions ADD COLUMN memory TEXT NOT NULL DEFAULT ''`)

	// Migration: add per-prompt model/effort override columns to prompt_queue.
	// These carry a temporary override chosen for a specific queued message so
	// it is honored when the queue is later drained (empty = use session default).
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN model TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN effort TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN context_tier TEXT NOT NULL DEFAULT ''`)

	// Migration: add a source column to prompt_queue so the drain path can tell
	// scheduler-originated prompts apart from user-typed ones (empty = user).
	// Scheduled prompts are recorded as a train-of-thought turn rather than a
	// user message when drained.
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN source TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN source_session_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN source_name TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE conversation_turns ADD COLUMN source_session_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE conversation_turns ADD COLUMN source_name TEXT NOT NULL DEFAULT ''`)

	// Migration: custom-compaction support. agent_session_id decouples the
	// underlying agent CLI session from the James session so it can be
	// substituted on compaction; existing rows default to their own
	// session_id (current behavior). compaction_mode defaults to 'agent' for
	// existing sessions (business as usual); new sessions are created as
	// 'custom'. context_tokens/context_window track context size.
	db.Exec(`ALTER TABLE sessions ADD COLUMN agent_session_id TEXT NOT NULL DEFAULT ''`)
	db.Exec(`UPDATE sessions SET agent_session_id = session_id WHERE agent_session_id = ''`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN compaction_mode TEXT NOT NULL DEFAULT 'agent'`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN opencode_cost REAL NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN environment TEXT NOT NULL DEFAULT '{}'`)

	// Migration: reply_channel_id routes a run's final response to an external
	// communication channel (channels.id). 0 = no channel routing. Present on
	// prompt_queue (channel-originated or channel-routed queued prompts) and on
	// schedules (scheduled prompt whose output is delivered to a channel).
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN reply_channel_id INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE schedules ADD COLUMN reply_channel_id INTEGER NOT NULL DEFAULT 0`)

	// Channel @mention gating: only forward inbound messages containing the
	// configured mention (empty = forward all); allow_anyone controls whether
	// messages from senders other than the signed-in owner are accepted.
	db.Exec(`ALTER TABLE channels ADD COLUMN mention TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE channels ADD COLUMN allow_anyone INTEGER NOT NULL DEFAULT 0`)

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

	if sess.AgentSessionID == "" {
		sess.AgentSessionID = sess.SessionID
	}
	if sess.CompactionMode == "" {
		sess.CompactionMode = CompactionCustom
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, status, agent_session_id, compaction_mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.SessionID, sess.Name, sess.Agent, sess.SystemPrompt, sess.Model, sess.Effort, sess.ContextTier, yolo, sess.Path, sess.Environment, sess.Status, sess.AgentSessionID, sess.CompactionMode, now, now,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID. Returns nil, nil if not found.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, status, memory, agent_session_id, compaction_mode, context_tokens, context_window, opencode_cost, created_at, updated_at
		 FROM sessions WHERE session_id = ?`, sessionID,
	)

	sess := &Session{}
	var yolo int
	err := row.Scan(
		&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt, &sess.Model, &sess.Effort, &sess.ContextTier,
		&yolo, &sess.Path, &sess.Environment, &sess.Status, &sess.Memory, &sess.AgentSessionID, &sess.CompactionMode,
		&sess.ContextTokens, &sess.ContextWindow, &sess.OpenCodeCost, &sess.CreatedAt, &sess.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess.Yolo = yolo != 0
	if sess.AgentSessionID == "" {
		sess.AgentSessionID = sess.SessionID
	}
	return sess, nil
}

// ListSessions returns all sessions.
func (s *Store) ListSessions() ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, status, memory, agent_session_id, compaction_mode, context_tokens, context_window, opencode_cost, created_at, updated_at
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
			&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt, &sess.Model, &sess.Effort, &sess.ContextTier,
			&yolo, &sess.Path, &sess.Environment, &sess.Status, &sess.Memory, &sess.AgentSessionID, &sess.CompactionMode,
			&sess.ContextTokens, &sess.ContextWindow, &sess.OpenCodeCost, &sess.CreatedAt, &sess.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.Yolo = yolo != 0
		if sess.AgentSessionID == "" {
			sess.AgentSessionID = sess.SessionID
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// UpdateSessionFields updates specific fields of a session.
func (s *Store) UpdateSessionFields(sessionID string, name, systemPrompt, model, effort, contextTier, path, compactionMode, environment *string, yolo *bool) error {
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
	if model != nil {
		sess.Model = *model
	}
	if effort != nil {
		sess.Effort = *effort
	}
	if contextTier != nil {
		sess.ContextTier = *contextTier
	}
	if path != nil {
		sess.Path = *path
	}
	if yolo != nil {
		sess.Yolo = *yolo
	}
	if compactionMode != nil {
		sess.CompactionMode = *compactionMode
	}
	if environment != nil {
		sess.Environment = *environment
	}

	now := time.Now().UTC()
	yoloInt := 0
	if sess.Yolo {
		yoloInt = 1
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET name = ?, system_prompt = ?, model = ?, effort = ?, context_tier = ?, yolo = ?, path = ?, compaction_mode = ?, environment = ?, updated_at = ? WHERE session_id = ?`,
		sess.Name, sess.SystemPrompt, sess.Model, sess.Effort, sess.ContextTier, yoloInt, sess.Path, sess.CompactionMode, sess.Environment, now, sessionID,
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

// SetAgentSessionID replaces the underlying agent CLI session id. Used by
// custom compaction to substitute a fresh underlying agent session while
// keeping the stable James session.
func (s *Store) SetAgentSessionID(sessionID, agentSessionID string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE sessions SET agent_session_id = ?, updated_at = ? WHERE session_id = ?`,
		agentSessionID, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("set agent session id: %w", err)
	}
	return nil
}

// SetContextUsage records the last measured/estimated context size and the
// model's context window for a session.
func (s *Store) SetContextUsage(sessionID string, tokens, window int) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE sessions SET context_tokens = ?, context_window = ?, updated_at = ? WHERE session_id = ?`,
		tokens, window, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("set context usage: %w", err)
	}
	return nil
}

// AddOpenCodeCost adds a provider-reported OpenCode invocation cost to the
// persistent session total.
func (s *Store) AddOpenCodeCost(sessionID string, cost float64) error {
	if cost == 0 {
		return nil
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE sessions SET opencode_cost = opencode_cost + ?, updated_at = ? WHERE session_id = ?`,
		cost, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("add OpenCode cost: %w", err)
	}
	return nil
}

// GetMemory returns the memory content for a session.
func (s *Store) GetMemory(sessionID string) (string, error) {
	var memory string
	err := s.db.QueryRow(`SELECT memory FROM sessions WHERE session_id = ?`, sessionID).Scan(&memory)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	if err != nil {
		return "", fmt.Errorf("get memory: %w", err)
	}
	return memory, nil
}

// SetMemory replaces the memory content for a session.
func (s *Store) SetMemory(sessionID, content string) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE sessions SET memory = ?, updated_at = ? WHERE session_id = ?`,
		content, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("set memory: %w", err)
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

// ResetWorkingSessions resets all sessions stuck in the working state back to
// idle. Agent processes are tracked in memory and do not survive a daemon
// restart, so any session left as working at startup is stale. Resetting them
// to idle lets the scheduler dispatch due prompts directly and prevents
// sessions from being permanently stuck busy after a crash. Returns the number
// of sessions reset.
func (s *Store) ResetWorkingSessions() (int64, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE sessions SET status = ?, updated_at = ? WHERE status = ?`,
		StateIdle, now, StateWorking,
	)
	if err != nil {
		return 0, fmt.Errorf("reset working sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
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
// DeleteLastTurnIfMatches deletes the most recent conversation turn for the
// session if its role and content match the given values. Used to dedupe the
// final assistant response against the trailing intermediate-text turn the
// streaming parser emits (which often contains the same text). Returns true
// if a turn was deleted.
func (s *Store) DeleteLastTurnIfMatches(sessionID, role, content string) (bool, error) {
	var id int64
	var foundRole, foundContent string
	err := s.db.QueryRow(
		`SELECT id, role, content FROM conversation_turns
		 WHERE session_id = ?
		 ORDER BY id DESC LIMIT 1`,
		sessionID,
	).Scan(&id, &foundRole, &foundContent)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query last turn: %w", err)
	}
	if foundRole != role || foundContent != content {
		return false, nil
	}
	if _, err := s.db.Exec(`DELETE FROM conversation_turns WHERE id = ?`, id); err != nil {
		return false, fmt.Errorf("delete last turn: %w", err)
	}
	return true, nil
}

func (s *Store) AddConversationTurn(sessionID string, role string, content string) error {
	return s.AddConversationTurnFrom(sessionID, role, content, "", "")
}

// AddConversationTurnFrom records a turn and, when supplied, the James agent
// session that originated it through a gadget command.
func (s *Store) AddConversationTurnFrom(sessionID, role, content, sourceSessionID, sourceName string) error {
	result, err := s.db.Exec(
		`INSERT INTO conversation_turns (session_id, role, content, source_session_id, source_name) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, sourceSessionID, sourceName,
	)
	if err != nil {
		return fmt.Errorf("add conversation turn: %w", err)
	}

	// Send notification about new message
	if s.notifyWriter != nil {
		turnIndex, _ := result.LastInsertId()
		_ = s.notifyWriter.Send(envelope.EventChatMessage, sessionID, map[string]interface{}{
			"role":              role,
			"content":           content,
			"source_session_id": sourceSessionID,
			"source_name":       sourceName,
			"timestamp":         time.Now().Format(time.RFC3339),
			"turn_index":        int(turnIndex),
		})
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
		`SELECT id, session_id, role, content, source_session_id, source_name, created_at
		 FROM conversation_turns WHERE session_id = ? ORDER BY created_at, id`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	defer rows.Close()

	var turns []*ConversationTurn
	for rows.Next() {
		t := &ConversationTurn{}
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.SourceSessionID, &t.SourceName, &t.CreatedAt); err != nil {
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
		`SELECT id, session_id, role, content, source_session_id, source_name, created_at FROM (
			SELECT id, session_id, role, content, source_session_id, source_name, created_at
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
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Role, &t.Content, &t.SourceSessionID, &t.SourceName, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// QueuedPrompt is a queued prompt with its optional per-prompt model/effort
// override (empty strings mean "use the session default"). Source records the
// prompt's origin ("" = user-typed, "scheduled" = scheduler-fired) so the drain
// path can classify the resulting conversation turn.
type QueuedPrompt struct {
	Prompt          string
	Model           string
	Effort          string
	ContextTier     string
	Source          string
	SourceSessionID string
	SourceName      string
	// ReplyChannelID routes this prompt's response to an external channel
	// (channels.id). 0 = no channel routing.
	ReplyChannelID int64
}

// QueuePrompt adds a prompt to the queue for a session. The model/effort/
// context-tier override (may be empty) is stored so a temporary override chosen
// while the session was busy is honored when the queue is drained. source
// records the prompt's origin ("" for user-typed, "scheduled" for
// scheduler-fired, "channel" for external-channel-originated).
func (s *Store) QueuePrompt(sessionID, prompt, model, effort, contextTier, source string) error {
	return s.QueuePromptChannel(sessionID, prompt, model, effort, contextTier, source, 0)
}

// QueuePromptChannel is QueuePrompt with an explicit reply channel id (0 = none).
func (s *Store) QueuePromptChannel(sessionID, prompt, model, effort, contextTier, source string, replyChannelID int64) error {
	return s.QueuePromptChannelFrom(sessionID, prompt, model, effort, contextTier, source, "", "", replyChannelID)
}

// QueuePromptChannelFrom is QueuePromptChannel with agent-origin provenance.
func (s *Store) QueuePromptChannelFrom(sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName string, replyChannelID int64) error {
	_, err := s.db.Exec(
		`INSERT INTO prompt_queue (session_id, prompt, model, effort, context_tier, source, source_session_id, source_name, reply_channel_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName, replyChannelID,
	)
	if err != nil {
		return fmt.Errorf("queue prompt: %w", err)
	}
	return nil
}

// DrainQueueGroup removes and returns the leading contiguous run of queued
// prompts that share the same model/effort override (ordered by creation time),
// leaving the remainder untouched. Processing one override-group per call (and
// re-invoking after each agent run) lets distinct overrides be honored without
// ever re-inserting prompts — so ordering is preserved and a later-arriving
// prompt can never jump ahead of the remainder. Returns an empty slice when the
// queue is empty.
func (s *Store) DrainQueueGroup(sessionID string) ([]QueuedPrompt, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("drain queue group: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id, prompt, model, effort, context_tier, source, source_session_id, source_name, reply_channel_id FROM prompt_queue WHERE session_id = ? ORDER BY created_at, id`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("drain queue group: %w", err)
	}

	var ids []int64
	var prompts []QueuedPrompt
	var haveFirst bool
	var firstModel, firstEffort, firstTier string
	var firstChannel int64
	for rows.Next() {
		var id int64
		var qp QueuedPrompt
		if err := rows.Scan(&id, &qp.Prompt, &qp.Model, &qp.Effort, &qp.ContextTier, &qp.Source, &qp.SourceSessionID, &qp.SourceName, &qp.ReplyChannelID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan queued prompt: %w", err)
		}
		if !haveFirst {
			haveFirst = true
			firstModel, firstEffort, firstTier = qp.Model, qp.Effort, qp.ContextTier
			firstChannel = qp.ReplyChannelID
		} else if qp.Model != firstModel || qp.Effort != firstEffort || qp.ContextTier != firstTier || qp.ReplyChannelID != firstChannel {
			// Different override or reply channel: end of the leading group. Keeping
			// reply channel in the grouping key ensures a group's response is routed
			// to exactly one channel (or none).
			break
		}
		ids = append(ids, id)
		prompts = append(prompts, qp)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, id := range ids {
		if _, err := tx.Exec(`DELETE FROM prompt_queue WHERE id = ?`, id); err != nil {
			return nil, fmt.Errorf("delete queued prompt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit drain queue group: %w", err)
	}

	return prompts, nil
}

// DrainQueue removes and returns all queued prompts for a session, ordered by creation time.
func (s *Store) DrainQueue(sessionID string) ([]QueuedPrompt, error) {
	rows, err := s.db.Query(
		`SELECT id, prompt, model, effort, context_tier, source, reply_channel_id FROM prompt_queue WHERE session_id = ? ORDER BY created_at, id`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("drain queue: %w", err)
	}
	defer rows.Close()

	var ids []int64
	var prompts []QueuedPrompt
	for rows.Next() {
		var id int64
		var qp QueuedPrompt
		if err := rows.Scan(&id, &qp.Prompt, &qp.Model, &qp.Effort, &qp.ContextTier, &qp.Source, &qp.ReplyChannelID); err != nil {
			return nil, fmt.Errorf("scan queued prompt: %w", err)
		}
		ids = append(ids, id)
		prompts = append(prompts, qp)
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
	return s.CreateScheduleFull(sessionID, prompt, scheduledAt, "", 0)
}

// CreateScheduleWithCron adds a scheduled prompt with an optional cron expression for recurrence.
func (s *Store) CreateScheduleWithCron(sessionID, prompt string, scheduledAt time.Time, cronExpr string) (int64, error) {
	return s.CreateScheduleFull(sessionID, prompt, scheduledAt, cronExpr, 0)
}

// CreateScheduleFull adds a scheduled prompt with optional cron recurrence and an
// optional reply channel id (0 = none) whose output is delivered to that channel.
func (s *Store) CreateScheduleFull(sessionID, prompt string, scheduledAt time.Time, cronExpr string, replyChannelID int64) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO schedules (session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, prompt, scheduledAt.UTC(), SchedulePending, cronExpr, replyChannelID,
	)
	if err != nil {
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	return res.LastInsertId()
}

// GetSchedule retrieves a schedule by ID.
func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, created_at FROM schedules WHERE id = ?`, id,
	)
	sch := &Schedule{}
	err := row.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &sch.CreatedAt)
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
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, created_at
			 FROM schedules WHERE session_id = ? AND status = ? ORDER BY scheduled_at`, sessionID, statusFilter,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, created_at
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
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &sch.CreatedAt); err != nil {
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
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, created_at
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
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &sch.CreatedAt); err != nil {
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

// UpdateSchedule edits the prompt, next-run time, cron expression, and reply
// channel of an existing pending schedule in place (preserving its ID). Only
// pending schedules can be edited. Returns an error if the schedule does not
// exist or is not pending.
func (s *Store) UpdateSchedule(id int64, prompt string, scheduledAt time.Time, cronExpr string, replyChannelID int64) error {
	res, err := s.db.Exec(
		`UPDATE schedules SET prompt = ?, scheduled_at = ?, cron_expr = ?, reply_channel_id = ? WHERE id = ? AND status = ?`,
		prompt, scheduledAt.UTC(), cronExpr, replyChannelID, id, SchedulePending,
	)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schedule %d not found or not pending", id)
	}
	return nil
}

// Channel binds a session to an external communication channel (a Teams chat,
// email thread, ...). The manager polls enabled channels for new messages and
// mirrors the session's responses back through channel_outbox.
type Channel struct {
	ID          int64
	SessionID   string
	Provider    string
	TargetID    string
	TargetLabel string
	Enabled     bool
	// Mention, when non-empty, gates inbound forwarding: only messages whose
	// text contains this name (optionally prefixed with '@') are forwarded, and
	// the mention token is stripped before forwarding. Empty = forward all.
	Mention string
	// AllowAnyone, when false (default), only forwards messages from the
	// signed-in owner (the account driving the provider). When true, messages
	// from any sender are forwarded.
	AllowAnyone bool
	// LastSeenID/LastSeenTS form the poll cursor: only messages strictly newer
	// than LastSeenTS are forwarded. Initialized at bind time to the target's
	// latest message so pre-existing history is not replayed.
	LastSeenID string
	LastSeenTS string
	// LastActivity is when a message was last seen or sent on this channel;
	// drives the fast (active) vs slow (idle) polling cadence.
	LastActivity time.Time
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateChannel inserts a channel binding and returns its id.
func (s *Store) CreateChannel(ch *Channel) (int64, error) {
	enabled := 1
	if !ch.Enabled {
		enabled = 0
	}
	allowAnyone := 0
	if ch.AllowAnyone {
		allowAnyone = 1
	}
	res, err := s.db.Exec(
		`INSERT INTO channels (session_id, provider, target_id, target_label, enabled, mention, allow_anyone, last_seen_id, last_seen_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.SessionID, ch.Provider, ch.TargetID, ch.TargetLabel, enabled, ch.Mention, allowAnyone, ch.LastSeenID, ch.LastSeenTS,
	)
	if err != nil {
		return 0, fmt.Errorf("create channel: %w", err)
	}
	return res.LastInsertId()
}

func scanChannel(sc interface {
	Scan(dest ...interface{}) error
}) (*Channel, error) {
	ch := &Channel{}
	var enabled int
	var allowAnyone int
	var lastActivity sql.NullTime
	if err := sc.Scan(&ch.ID, &ch.SessionID, &ch.Provider, &ch.TargetID, &ch.TargetLabel, &enabled,
		&ch.Mention, &allowAnyone, &ch.LastSeenID, &ch.LastSeenTS, &lastActivity, &ch.LastError, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
		return nil, err
	}
	ch.Enabled = enabled != 0
	ch.AllowAnyone = allowAnyone != 0
	if lastActivity.Valid {
		ch.LastActivity = lastActivity.Time
	}
	return ch, nil
}

const channelColumns = `id, session_id, provider, target_id, target_label, enabled, mention, allow_anyone, last_seen_id, last_seen_ts, last_activity_at, last_error, created_at, updated_at`

// GetChannel retrieves a channel by id (nil if not found).
func (s *Store) GetChannel(id int64) (*Channel, error) {
	row := s.db.QueryRow(`SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	ch, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}

// ListChannels returns channels for a session, or all channels when sessionID
// is empty.
func (s *Store) ListChannels(sessionID string) ([]*Channel, error) {
	var rows *sql.Rows
	var err error
	if sessionID == "" {
		rows, err = s.db.Query(`SELECT ` + channelColumns + ` FROM channels ORDER BY id`)
	} else {
		rows, err = s.db.Query(`SELECT `+channelColumns+` FROM channels WHERE session_id = ? ORDER BY id`, sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// ListEnabledChannels returns all enabled channels (for the poll loop).
func (s *Store) ListEnabledChannels() ([]*Channel, error) {
	rows, err := s.db.Query(`SELECT ` + channelColumns + ` FROM channels WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled channels: %w", err)
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// DeleteChannel removes a channel binding (and its outbox via cascade).
func (s *Store) DeleteChannel(id int64) error {
	res, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("channel %d not found", id)
	}
	return nil
}

// SetChannelEnabled toggles a channel's enabled flag.
func (s *Store) SetChannelEnabled(id int64, enabled bool) error {
	e := 0
	if enabled {
		e = 1
	}
	res, err := s.db.Exec(`UPDATE channels SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, e, id)
	if err != nil {
		return fmt.Errorf("set channel enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("channel %d not found", id)
	}
	return nil
}

// UpdateChannelMention sets a channel's @mention gate (empty = forward all).
func (s *Store) UpdateChannelMention(id int64, mention string) error {
	res, err := s.db.Exec(`UPDATE channels SET mention = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, mention, id)
	if err != nil {
		return fmt.Errorf("update channel mention: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("channel %d not found", id)
	}
	return nil
}

// SetChannelAllowAnyone toggles whether messages from senders other than the
// signed-in owner are forwarded.
func (s *Store) SetChannelAllowAnyone(id int64, allow bool) error {
	a := 0
	if allow {
		a = 1
	}
	res, err := s.db.Exec(`UPDATE channels SET allow_anyone = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, a, id)
	if err != nil {
		return fmt.Errorf("set channel allow_anyone: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("channel %d not found", id)
	}
	return nil
}

// UpdateChannelCursor advances a channel's poll cursor.
func (s *Store) UpdateChannelCursor(id int64, lastSeenID, lastSeenTS string) error {
	_, err := s.db.Exec(
		`UPDATE channels SET last_seen_id = ?, last_seen_ts = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		lastSeenID, lastSeenTS, id,
	)
	if err != nil {
		return fmt.Errorf("update channel cursor: %w", err)
	}
	return nil
}

// TouchChannelActivity records channel activity now (drives fast polling).
func (s *Store) TouchChannelActivity(id int64) error {
	_, err := s.db.Exec(`UPDATE channels SET last_activity_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("touch channel activity: %w", err)
	}
	return nil
}

// SetChannelError records (or clears, when msg is empty) a channel's last error.
func (s *Store) SetChannelError(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE channels SET last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, msg, id)
	if err != nil {
		return fmt.Errorf("set channel error: %w", err)
	}
	return nil
}

// OutboxItem is a pending outbound channel message joined with its channel's
// provider/target context.
type OutboxItem struct {
	ID        int64
	ChannelID int64
	Provider  string
	TargetID  string
	Content   string
}

// EnqueueOutbox queues an outbound message for a channel.
func (s *Store) EnqueueOutbox(channelID int64, content string) error {
	_, err := s.db.Exec(`INSERT INTO channel_outbox (channel_id, content) VALUES (?, ?)`, channelID, content)
	if err != nil {
		return fmt.Errorf("enqueue outbox: %w", err)
	}
	return nil
}

// PendingOutbox returns pending outbound messages with channel context, oldest
// first. Only messages for enabled channels are returned.
func (s *Store) PendingOutbox() ([]OutboxItem, error) {
	rows, err := s.db.Query(
		`SELECT o.id, o.channel_id, c.provider, c.target_id, o.content
		 FROM channel_outbox o JOIN channels c ON c.id = o.channel_id
		 WHERE o.status = 'pending' AND c.enabled = 1 ORDER BY o.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("pending outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.ID, &it.ChannelID, &it.Provider, &it.TargetID, &it.Content); err != nil {
			return nil, fmt.Errorf("scan outbox: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarkOutboxSent marks an outbox item as sent, recording the provider message id.
func (s *Store) MarkOutboxSent(id int64, sentMsgID string) error {
	_, err := s.db.Exec(`UPDATE channel_outbox SET status = 'sent', sent_msg_id = ? WHERE id = ?`, sentMsgID, id)
	if err != nil {
		return fmt.Errorf("mark outbox sent: %w", err)
	}
	return nil
}

// MarkOutboxError marks an outbox item as failed with an error message.
func (s *Store) MarkOutboxError(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE channel_outbox SET status = 'error', error = ? WHERE id = ?`, msg, id)
	if err != nil {
		return fmt.Errorf("mark outbox error: %w", err)
	}
	return nil
}

// SentMessageIDs returns the set of provider message ids sent by us for a
// channel, used to suppress echo (skip our own messages during polling).
func (s *Store) SentMessageIDs(channelID int64) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT sent_msg_id FROM channel_outbox WHERE channel_id = ? AND sent_msg_id != ''`, channelID)
	if err != nil {
		return nil, fmt.Errorf("sent message ids: %w", err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}
