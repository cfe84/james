package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
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
	ContextTier        string
	Yolo               bool
	Path               string
	Environment        string
	GadgetRoute        string
	GadgetCapabilities string
	Status             string
	// AgentSessionID is the session id handed to the underlying agent CLI
	// (claude/copilot) via --session-id/--resume. It is decoupled from
	// SessionID so custom compaction can substitute a fresh underlying agent
	// session while keeping the stable James session. Defaults to SessionID.
	AgentSessionID string
	// CompactionMode is "agent" (rely on the agent's built-in compaction) or
	// "custom" (James-managed distillation/summary/substitution).
	CompactionMode string
	// CompactionThresholdTokens is the persisted absolute context-token count
	// at which custom compaction runs.
	CompactionThresholdTokens int
	// ContextTokens is the last measured/estimated underlying-context size and
	// ContextWindow the model's max context. Used to trigger custom compaction
	// at a threshold and to surface usage in clients.
	ContextTokens int
	ContextWindow int
	// OpenCodeCost is the cumulative provider-reported USD cost for this
	// OpenCode session. It remains zero for other agents.
	OpenCodeCost float64
	// ScheduleReadyAt records the completion time of the latest scheduled run
	// configured to surface its result as ready. A zero value means none.
	ScheduleReadyAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        int64
	Generation      int64
}

func (s *Store) GetClientOperation(sessionID, operationID string) (ClientOperation, bool, error) {
	var op ClientOperation
	err := s.db.QueryRow(`SELECT session_id, operation_id, digest, status, response_json FROM client_operations WHERE session_id = ? AND operation_id = ?`, sessionID, operationID).
		Scan(&op.SessionID, &op.OperationID, &op.Digest, &op.Status, &op.Response)
	if err == sql.ErrNoRows {
		return ClientOperation{}, false, nil
	}
	if err != nil {
		return ClientOperation{}, false, fmt.Errorf("read operation: %w", err)
	}
	return op, true, nil
}

func (s *Store) UpdateClientOperationStatus(sessionID, operationID, from, to string, response any) (bool, error) {
	raw := ""
	if response != nil {
		encoded, err := json.Marshal(response)
		if err != nil {
			return false, fmt.Errorf("marshal operation response: %w", err)
		}
		raw = string(encoded)
	}
	res, err := s.db.Exec(`UPDATE client_operations SET status = ?, response_json = CASE WHEN ? <> '' THEN ? ELSE response_json END, updated_at = CURRENT_TIMESTAMP
		WHERE session_id = ? AND operation_id = ? AND status = ?`, to, raw, raw, sessionID, operationID, from)
	if err != nil {
		return false, fmt.Errorf("update operation status: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
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
	Role            string // user, assistant, scheduled, callback, system, compaction, or distillation outcome
	Content         string
	SourceSessionID string
	SourceName      string
	CreatedAt       time.Time
}

// DistillationProgress is durable work state kept outside the conversation.
// SnapshotMaxTurnID prevents a retry from silently mixing a moving transcript.
type DistillationProgress struct {
	SessionID         string
	SnapshotMaxTurnID int64
	ConfigKey         string
	NextChunk         int
	ChunkCount        int
}

// CommitCompactionHandoff atomically publishes a successful fresh agent
// session. Until this commits, the old underlying session remains resumable.
// The marker and summary are written in the same transaction as the metadata
// handoff so readers never observe half of a compaction.
func (s *Store) CommitCompactionHandoff(sessionID, agentSessionID string, contextWindow int, summary string) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(agentSessionID) == "" ||
		contextWindow <= 0 || strings.TrimSpace(summary) == "" {
		return fmt.Errorf("invalid compaction handoff")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin compaction handoff: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	res, err := tx.Exec(`UPDATE sessions SET agent_session_id = ?, context_tokens = 0,
		context_window = ?, revision = revision + 1 + ?, updated_at = ? WHERE session_id = ?`,
		agentSessionID, contextWindow, 1+boolInt(strings.TrimSpace(summary) != ""), now, sessionID)
	if err != nil {
		return fmt.Errorf("publish compaction session: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("compaction handoff rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("session %q not found", sessionID)
	}
	if _, err := tx.Exec(`INSERT INTO conversation_turns (session_id, role, content)
		VALUES (?, 'compaction', ?)`, sessionID, "compacted"); err != nil {
		return fmt.Errorf("record compaction marker: %w", err)
	}
	if strings.TrimSpace(summary) != "" {
		if _, err := tx.Exec(`INSERT INTO conversation_turns (session_id, role, content)
			VALUES (?, 'compaction_summary', ?)`, sessionID, summary); err != nil {
			return fmt.Errorf("record compaction summary: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit compaction handoff: %w", err)
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// BeginClientOperation atomically reserves an operation id and stores the
// accepted response before any agent/queue work is started.
func (s *Store) BeginClientOperation(sessionID, operationID, digest string, response any) (ClientOperation, bool, error) {
	if sessionID == "" || operationID == "" || digest == "" {
		return ClientOperation{}, false, fmt.Errorf("operation identity is required")
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return ClientOperation{}, false, fmt.Errorf("marshal operation response: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return ClientOperation{}, false, fmt.Errorf("begin operation: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT OR IGNORE INTO client_operations (session_id, operation_id, digest, status, response_json) VALUES (?, ?, ?, ?, ?)`,
		sessionID, operationID, digest, OperationAccepted, string(raw))
	if err != nil {
		return ClientOperation{}, false, fmt.Errorf("persist operation: %w", err)
	}
	var existing ClientOperation
	if affected, _ := result.RowsAffected(); affected == 0 {
		err = tx.QueryRow(`SELECT session_id, operation_id, digest, status, response_json FROM client_operations WHERE session_id = ? AND operation_id = ?`, sessionID, operationID).
			Scan(&existing.SessionID, &existing.OperationID, &existing.Digest, &existing.Status, &existing.Response)
		if err != nil {
			return ClientOperation{}, false, fmt.Errorf("read operation after concurrent reservation: %w", err)
		}
		if existing.Digest != digest {
			return ClientOperation{}, false, fmt.Errorf("operation_id %q was already used with different input", operationID)
		}
		if err := tx.Commit(); err != nil {
			return ClientOperation{}, false, fmt.Errorf("commit operation lookup: %w", err)
		}
		return existing, true, nil
	}
	if err := tx.Commit(); err != nil {
		return ClientOperation{}, false, fmt.Errorf("commit operation: %w", err)
	}
	return ClientOperation{SessionID: sessionID, OperationID: operationID, Digest: digest, Status: OperationAccepted, Response: raw}, false, nil
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
	MarkReady   bool   // true = surface the completed scheduled result as ready
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

const (
	OperationPending   = "pending"
	OperationAccepted  = "accepted"
	OperationQueued    = "queued"
	OperationRunning   = "running"
	OperationCompleted = "completed"
	OperationFailed    = "failed"
)

// ClientOperation is the durable idempotency record for a caller operation.
type ClientOperation struct {
	SessionID   string
	OperationID string
	Digest      string
	Status      string
	Response    []byte
}

// New opens (or creates) the SQLite database at the given path and runs migrations.
func New(dbPath string) (*Store, error) {
	dsn := "file::memory:?mode=memory"
	if dbPath != ":memory:" {
		path, err := filepath.Abs(dbPath)
		if err != nil {
			return nil, fmt.Errorf("resolve database path: %w", err)
		}
		path = filepath.ToSlash(path)
		if filepath.VolumeName(path) != "" {
			path = "/" + path
		}
		u := url.URL{Scheme: "file", Path: path}
		dsn = u.String() + "?"
	} else {
		dsn += "&"
	}
	// Apply connection-local settings to every connection, including write
	// reservation before read/modify/write transactions (avoids WAL snapshot upgrades).
	db, err := sql.Open("sqlite3", dsn+"_busy_timeout=5000&_foreign_keys=on&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	if dbPath == ":memory:" {
		db.SetMaxOpenConns(1)
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
    compaction_threshold_tokens INTEGER NOT NULL DEFAULT 150000,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
    ,revision INTEGER NOT NULL DEFAULT 0
    ,generation INTEGER NOT NULL DEFAULT 1
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
CREATE INDEX IF NOT EXISTS idx_conversation_session_created ON conversation_turns(session_id, created_at, id);

CREATE TABLE IF NOT EXISTS distillation_progress (
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    snapshot_max_turn_id INTEGER NOT NULL,
    config_key TEXT NOT NULL,
    next_chunk INTEGER NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, snapshot_max_turn_id, config_key)
);

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

CREATE TABLE IF NOT EXISTS client_operations (
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL,
    digest TEXT NOT NULL,
    status TEXT NOT NULL,
    response_json TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_id, operation_id)
);

CREATE TABLE IF NOT EXISTS schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    prompt TEXT NOT NULL,
    scheduled_at DATETIME NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    cron_expr TEXT NOT NULL DEFAULT '',
    mark_ready INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_schedules_session ON schedules(session_id);
CREATE INDEX IF NOT EXISTS idx_schedules_pending ON schedules(status, scheduled_at);

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
	db.Exec(`ALTER TABLE schedules ADD COLUMN mark_ready INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN schedule_ready_at DATETIME`)
	db.Exec(`CREATE TABLE IF NOT EXISTS client_operations (
		session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		operation_id TEXT NOT NULL, digest TEXT NOT NULL, status TEXT NOT NULL,
		response_json TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (session_id, operation_id))`)

	// Migration: add model column to sessions if missing.
	db.Exec(`ALTER TABLE sessions ADD COLUMN model TEXT NOT NULL DEFAULT ''`)

	// Migration: add effort column to sessions if missing.
	db.Exec(`ALTER TABLE sessions ADD COLUMN effort TEXT NOT NULL DEFAULT ''`)

	// Migration: add context_tier column to sessions if missing. Selects
	// copilot's context-window tier (e.g. "long_context"); empty = default.
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_tier TEXT NOT NULL DEFAULT ''`)

	// Migration: add memory column to sessions if missing.

	// Migration: add per-prompt model/effort override columns to prompt_queue.
	// These carry a temporary override chosen for a specific queued message so
	// it is honored when the queue is later drained (empty = use session default).
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN model TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN effort TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN context_tier TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN operation_id TEXT NOT NULL DEFAULT ''`)

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
	var hasCompactionThresholdTokens int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('sessions') WHERE name = 'compaction_threshold_tokens'`).Scan(&hasCompactionThresholdTokens); err != nil {
		return err
	}
	if hasCompactionThresholdTokens == 0 {
		if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN compaction_threshold_tokens INTEGER NOT NULL DEFAULT 150000`); err != nil {
			return err
		}
		// A newly added column has one fixed SQLite default. Resolve the
		// context-tier-specific default only for rows that did not previously
		// have a persisted token threshold. This runs once because the column
		// existence check makes the migration idempotent.
		if _, err := db.Exec(`UPDATE sessions SET compaction_threshold_tokens = 800000 WHERE context_tier = 'long_context'`); err != nil {
			return err
		}
	}
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN opencode_cost REAL NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN environment TEXT NOT NULL DEFAULT '{}'`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN gadget_capabilities TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN gadget_route TEXT NOT NULL DEFAULT '{}'`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE sessions ADD COLUMN generation INTEGER NOT NULL DEFAULT 1`)

	// Migration: reply_channel_id routes a run's final response to an external
	// communication channel (channels.id). 0 = no channel routing. Present on
	// prompt_queue (channel-originated or channel-routed queued prompts) and on
	// schedules (scheduled prompt whose output is delivered to a channel).
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN reply_channel_id INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE prompt_queue ADD COLUMN mark_ready INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE schedules ADD COLUMN reply_channel_id INTEGER NOT NULL DEFAULT 0`)

	// Channel @mention gating: only forward inbound messages containing the
	// configured mention (empty = forward all); allow_anyone controls whether
	// messages from senders other than the signed-in owner are accepted.
	db.Exec(`ALTER TABLE channels ADD COLUMN mention TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE channels ADD COLUMN allow_anyone INTEGER NOT NULL DEFAULT 0`)

	var hasScheduleID int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('prompt_queue') WHERE name = 'schedule_id'`).Scan(&hasScheduleID); err != nil {
		return err
	}
	if hasScheduleID == 0 {
		if _, err := db.Exec(`ALTER TABLE prompt_queue ADD COLUMN schedule_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	var hasOperationID int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('prompt_queue') WHERE name = 'operation_id'`).Scan(&hasOperationID); err != nil {
		return err
	}
	if hasOperationID == 0 {
		if _, err := db.Exec(`ALTER TABLE prompt_queue ADD COLUMN operation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_queue_recurring_schedule ON prompt_queue(schedule_id) WHERE schedule_id != 0;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_queue_operation ON prompt_queue(session_id, operation_id) WHERE operation_id != '';
		CREATE INDEX IF NOT EXISTS idx_queue_session_order ON prompt_queue(session_id, created_at, id);
		CREATE INDEX IF NOT EXISTS idx_queue_scheduled ON prompt_queue(session_id, source);
		CREATE INDEX IF NOT EXISTS idx_schedules_session_status ON schedules(session_id, status, scheduled_at, id);`)
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

	if sess.AgentSessionID == "" {
		sess.AgentSessionID = sess.SessionID
	}
	if sess.CompactionMode == "" {
		sess.CompactionMode = CompactionCustom
	}
	if sess.CompactionThresholdTokens == 0 {
		sess.CompactionThresholdTokens = envelope.DefaultCompactionThresholdTokensForContext(sess.ContextTier)
	}
	if err := envelope.ValidateCompactionThresholdTokens(sess.CompactionThresholdTokens); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	_, err := s.db.Exec(
		`INSERT INTO sessions (session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, gadget_route, gadget_capabilities, status, agent_session_id, compaction_mode, compaction_threshold_tokens, created_at, updated_at, revision, generation)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1)`,
		sess.SessionID, sess.Name, sess.Agent, sess.SystemPrompt, sess.Model, sess.Effort, sess.ContextTier, yolo, sess.Path, sess.Environment, sess.GadgetRoute, sess.GadgetCapabilities, sess.Status, sess.AgentSessionID, sess.CompactionMode, sess.CompactionThresholdTokens, now, now,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession retrieves a session by ID. Returns nil, nil if not found.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	row := s.db.QueryRow(
		`SELECT session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, gadget_route, gadget_capabilities, status, agent_session_id, compaction_mode, compaction_threshold_tokens, context_tokens, context_window, opencode_cost, schedule_ready_at, created_at, updated_at, revision, generation
		 FROM sessions WHERE session_id = ?`, sessionID,
	)

	sess := &Session{}
	var yolo int
	var scheduleReadyAt sql.NullTime
	err := row.Scan(
		&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt, &sess.Model, &sess.Effort, &sess.ContextTier,
		&yolo, &sess.Path, &sess.Environment, &sess.GadgetRoute, &sess.GadgetCapabilities, &sess.Status, &sess.AgentSessionID, &sess.CompactionMode, &sess.CompactionThresholdTokens,
		&sess.ContextTokens, &sess.ContextWindow, &sess.OpenCodeCost, &scheduleReadyAt, &sess.CreatedAt, &sess.UpdatedAt, &sess.Revision, &sess.Generation,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess.Yolo = yolo != 0
	if scheduleReadyAt.Valid {
		sess.ScheduleReadyAt = scheduleReadyAt.Time
	}
	if sess.AgentSessionID == "" {
		sess.AgentSessionID = sess.SessionID
	}
	sess.CompactionThresholdTokens = envelope.EffectiveCompactionThresholdTokens(sess.CompactionThresholdTokens, sess.ContextTier)
	if err := envelope.ValidateCompactionThresholdTokens(sess.CompactionThresholdTokens); err != nil {
		return nil, fmt.Errorf("get session: invalid compaction threshold: %w", err)
	}
	return sess, nil
}

// ListSessions returns all sessions.
func (s *Store) ListSessions() ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT session_id, name, agent, system_prompt, model, effort, context_tier, yolo, path, environment, gadget_route, gadget_capabilities, status, agent_session_id, compaction_mode, compaction_threshold_tokens, context_tokens, context_window, opencode_cost, schedule_ready_at, created_at, updated_at, revision, generation
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
		var scheduleReadyAt sql.NullTime
		if err := rows.Scan(
			&sess.SessionID, &sess.Name, &sess.Agent, &sess.SystemPrompt, &sess.Model, &sess.Effort, &sess.ContextTier,
			&yolo, &sess.Path, &sess.Environment, &sess.GadgetRoute, &sess.GadgetCapabilities, &sess.Status, &sess.AgentSessionID, &sess.CompactionMode, &sess.CompactionThresholdTokens,
			&sess.ContextTokens, &sess.ContextWindow, &sess.OpenCodeCost, &scheduleReadyAt, &sess.CreatedAt, &sess.UpdatedAt, &sess.Revision, &sess.Generation,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.Yolo = yolo != 0
		if scheduleReadyAt.Valid {
			sess.ScheduleReadyAt = scheduleReadyAt.Time
		}
		if sess.AgentSessionID == "" {
			sess.AgentSessionID = sess.SessionID
		}
		sess.CompactionThresholdTokens = envelope.EffectiveCompactionThresholdTokens(sess.CompactionThresholdTokens, sess.ContextTier)
		if err := envelope.ValidateCompactionThresholdTokens(sess.CompactionThresholdTokens); err != nil {
			return nil, fmt.Errorf("list sessions: invalid compaction threshold: %w", err)
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// UpdateSessionFields updates specific fields of a session.
func (s *Store) UpdateSessionFields(sessionID string, name, systemPrompt, model, effort, contextTier, path, compactionMode, environment, gadgetRoute *string, yolo *bool, gadgetCapabilities ...*string) error {
	return s.updateSessionFields(sessionID, name, systemPrompt, model, effort, contextTier, path, compactionMode, nil, environment, gadgetRoute, yolo, gadgetCapabilities...)
}

// UpdateSessionFieldsWithCompactionThresholdTokens updates session metadata,
// including the optional persisted custom-compaction token threshold.
func (s *Store) UpdateSessionFieldsWithCompactionThresholdTokens(sessionID string, name, systemPrompt, model, effort, contextTier, path, compactionMode *string, compactionThresholdTokens *int, environment, gadgetRoute *string, yolo *bool, gadgetCapabilities ...*string) error {
	return s.updateSessionFields(sessionID, name, systemPrompt, model, effort, contextTier, path, compactionMode, compactionThresholdTokens, environment, gadgetRoute, yolo, gadgetCapabilities...)
}

func (s *Store) updateSessionFields(sessionID string, name, systemPrompt, model, effort, contextTier, path, compactionMode *string, compactionThresholdTokens *int, environment, gadgetRoute *string, yolo *bool, gadgetCapabilities ...*string) error {
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
	if compactionThresholdTokens != nil {
		if err := envelope.ValidateCompactionThresholdTokens(*compactionThresholdTokens); err != nil {
			return fmt.Errorf("update session: %w", err)
		}
		sess.CompactionThresholdTokens = *compactionThresholdTokens
	}
	if environment != nil {
		sess.Environment = *environment
	}
	if gadgetRoute != nil {
		sess.GadgetRoute = *gadgetRoute
	}
	var capabilities *string
	if len(gadgetCapabilities) > 0 {
		capabilities = gadgetCapabilities[0]
	}

	now := time.Now().UTC()
	yoloInt := 0
	if sess.Yolo {
		yoloInt = 1
	}
	res, err := s.db.Exec(
		`UPDATE sessions SET name = ?, system_prompt = ?, model = ?, effort = ?, context_tier = ?, yolo = ?, path = ?, compaction_mode = ?, compaction_threshold_tokens = ?, environment = ?, gadget_route = ?, gadget_capabilities = COALESCE(?, gadget_capabilities), revision = revision + 1, updated_at = ? WHERE session_id = ?`,
		sess.Name, sess.SystemPrompt, sess.Model, sess.Effort, sess.ContextTier, yoloInt, sess.Path, sess.CompactionMode, sess.CompactionThresholdTokens, sess.Environment, sess.GadgetRoute, capabilities, now, sessionID,
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
		`UPDATE sessions SET agent_session_id = ?, revision = revision + 1, updated_at = ? WHERE session_id = ?`,
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
		`UPDATE sessions SET context_tokens = ?, context_window = ?, revision = revision + 1, updated_at = ? WHERE session_id = ?`,
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
		`UPDATE sessions SET opencode_cost = opencode_cost + ?, revision = revision + 1, updated_at = ? WHERE session_id = ?`,
		cost, now, sessionID,
	)
	if err != nil {
		return fmt.Errorf("add OpenCode cost: %w", err)
	}
	return nil
}

// UpdateSessionStatus updates the status of a session.
func (s *Store) UpdateSessionStatus(sessionID string, status string) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE sessions SET status = ?, revision = revision + 1, updated_at = ? WHERE session_id = ?`,
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
		`UPDATE sessions SET status = ?, revision = revision + 1, updated_at = ? WHERE status = ?`,
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
	tx, err := s.db.Begin()
	if err != nil {
		return false, fmt.Errorf("begin delete last turn tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM conversation_turns WHERE id = ? AND session_id = ?`, id, sessionID); err != nil {
		return false, fmt.Errorf("delete last turn: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET revision = revision + 1, updated_at = ? WHERE session_id = ?`, time.Now().UTC(), sessionID); err != nil {
		return false, fmt.Errorf("advance delete revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete last turn: %w", err)
	}
	return true, nil
}

func (s *Store) AddConversationTurn(sessionID string, role string, content string) error {
	return s.AddConversationTurnFrom(sessionID, role, content, "", "")
}

// AddConversationTurnFrom records a turn and, when supplied, the James agent
// session that originated it through a gadget command.
func (s *Store) AddConversationTurnFrom(sessionID, role, content, sourceSessionID, sourceName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin conversation turn tx: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(
		`INSERT INTO conversation_turns (session_id, role, content, source_session_id, source_name) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, sourceSessionID, sourceName,
	)
	if err != nil {
		return fmt.Errorf("add conversation turn: %w", err)
	}

	turnIndex, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("get conversation turn id: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET revision = revision + 1, updated_at = ? WHERE session_id = ?`, time.Now().UTC(), sessionID); err != nil {
		return fmt.Errorf("advance conversation revision: %w", err)
	}
	var revision, generation int64
	if err := tx.QueryRow(`SELECT revision, generation FROM sessions WHERE session_id = ?`, sessionID).Scan(&revision, &generation); err != nil {
		return fmt.Errorf("read conversation revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit conversation turn: %w", err)
	}
	s.notifyConversationTurn(sessionID, role, content, sourceSessionID, sourceName, turnIndex, revision, generation)
	return nil
}

func (s *Store) notifyConversationTurn(sessionID, role, content, sourceSessionID, sourceName string, turnIndex, revision, generation int64) {
	if s.notifyWriter != nil {
		_ = s.notifyWriter.SendAsync(envelope.EventChatMessage, sessionID, map[string]interface{}{
			"id":                turnIndex,
			"role":              role,
			"content":           content,
			"source_session_id": sourceSessionID,
			"source_name":       sourceName,
			"timestamp":         time.Now().Format(time.RFC3339),
			"turn_index":        int(turnIndex),
			"revision":          revision,
			"generation":        generation,
		})
	}
}

// SessionWatermark is the authoritative conversation cursor for a session.
type SessionWatermark struct {
	Revision   int64
	Generation int64
}

func (s *Store) GetSessionWatermark(sessionID string) (SessionWatermark, error) {
	var w SessionWatermark
	err := s.db.QueryRow(`SELECT revision, generation FROM sessions WHERE session_id = ?`, sessionID).Scan(&w.Revision, &w.Generation)
	if err == sql.ErrNoRows {
		return w, fmt.Errorf("session %q not found", sessionID)
	}
	return w, err
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

func (s *Store) GetDistillationProgress(sessionID string, snapshotMaxTurnID int64, configKey string) (*DistillationProgress, error) {
	var p DistillationProgress
	err := s.db.QueryRow(`SELECT session_id, snapshot_max_turn_id, config_key, next_chunk, chunk_count
		FROM distillation_progress WHERE session_id = ? AND snapshot_max_turn_id = ? AND config_key = ?`,
		sessionID, snapshotMaxTurnID, configKey).Scan(&p.SessionID, &p.SnapshotMaxTurnID, &p.ConfigKey, &p.NextChunk, &p.ChunkCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) PutDistillationProgress(p DistillationProgress) error {
	if strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.ConfigKey) == "" ||
		p.SnapshotMaxTurnID < 0 || p.NextChunk < 0 || p.ChunkCount < 0 || p.NextChunk > p.ChunkCount {
		return fmt.Errorf("invalid distillation progress")
	}
	_, err := s.db.Exec(`INSERT INTO distillation_progress
		(session_id, snapshot_max_turn_id, config_key, next_chunk, chunk_count, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id, snapshot_max_turn_id, config_key) DO UPDATE SET
		next_chunk=excluded.next_chunk, chunk_count=excluded.chunk_count, updated_at=CURRENT_TIMESTAMP`,
		p.SessionID, p.SnapshotMaxTurnID, p.ConfigKey, p.NextChunk, p.ChunkCount)
	return err
}

func (s *Store) ClearDistillationProgress(sessionID string, snapshotMaxTurnID int64, configKey string) error {
	_, err := s.db.Exec(`DELETE FROM distillation_progress WHERE session_id = ? AND snapshot_max_turn_id = ? AND config_key = ?`,
		sessionID, snapshotMaxTurnID, configKey)
	return err
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
	MarkReady      bool
	OperationID    string
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// QueuePrompt adds a prompt to the queue for a session. The model/effort/
// context-tier override (may be empty) is stored so a temporary override chosen
// while the session was busy is honored when the queue is drained. source
// records the prompt's origin ("" for user-typed, "scheduled" for
// scheduler-fired, "channel" for external-channel-originated).
func (s *Store) QueuePrompt(sessionID, prompt, model, effort, contextTier, source string) error {
	return s.QueuePromptChannel(sessionID, prompt, model, effort, contextTier, source, 0, false)
}

// QueuePromptChannel is QueuePrompt with an explicit reply channel id (0 = none).
func (s *Store) QueuePromptChannel(sessionID, prompt, model, effort, contextTier, source string, replyChannelID int64, markReady bool) error {
	return s.QueuePromptChannelFrom(sessionID, prompt, model, effort, contextTier, source, "", "", replyChannelID, markReady)
}

// QueuePromptChannelFrom is QueuePromptChannel with agent-origin provenance.
func (s *Store) QueuePromptChannelFrom(sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName string, replyChannelID int64, markReady bool) error {
	return s.QueuePromptChannelFromOperation(sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName, replyChannelID, markReady, "")
}

func (s *Store) QueuePromptChannelFromOperation(sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName string, replyChannelID int64, markReady bool, operationID string) error {
	_, err := s.db.Exec(
		`INSERT INTO prompt_queue (session_id, prompt, model, effort, context_tier, source, source_session_id, source_name, reply_channel_id, mark_ready, operation_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, prompt, model, effort, contextTier, source, sourceSessionID, sourceName, replyChannelID, boolToInt(markReady), operationID,
	)
	if err != nil {
		return fmt.Errorf("queue prompt: %w", err)
	}
	return nil
}

func (s *Store) HasQueuedOperation(sessionID, operationID string) (bool, error) {
	if operationID == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM prompt_queue WHERE session_id = ? AND operation_id = ?`, sessionID, operationID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check queued operation: %w", err)
	}
	return n != 0, nil
}

const (
	MaxQueueBatch      = 32
	MaxQueueBatchBytes = 1 << 20
)

// DrainQueueGroup removes a bounded leading group with matching overrides,
// reply channel and Ready setting, leaving the remainder in order. An empty
// queue releases the working session to idle in the same transaction.
func (s *Store) DrainQueueGroup(sessionID string) ([]QueuedPrompt, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("drain queue group: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT id, prompt, model, effort, context_tier, source, source_session_id, source_name, reply_channel_id, mark_ready, operation_id FROM prompt_queue WHERE session_id = ? ORDER BY created_at, id LIMIT ?`, sessionID, MaxQueueBatch,
	)
	if err != nil {
		return nil, fmt.Errorf("drain queue group: %w", err)
	}

	var ids []int64
	var prompts []QueuedPrompt
	var haveFirst bool
	var firstModel, firstEffort, firstTier string
	var firstChannel int64
	var firstOperationID string
	totalBytes := 0
	for rows.Next() {
		var id int64
		var qp QueuedPrompt
		var markReady int
		if err := rows.Scan(&id, &qp.Prompt, &qp.Model, &qp.Effort, &qp.ContextTier, &qp.Source, &qp.SourceSessionID, &qp.SourceName, &qp.ReplyChannelID, &markReady, &qp.OperationID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan queued prompt: %w", err)
		}
		qp.MarkReady = markReady != 0
		if len(prompts) > 0 && totalBytes+len(qp.Prompt) > MaxQueueBatchBytes {
			break
		}
		if !haveFirst {
			haveFirst = true
			firstModel, firstEffort, firstTier = qp.Model, qp.Effort, qp.ContextTier
			firstChannel = qp.ReplyChannelID
			firstOperationID = qp.OperationID
		} else if qp.Model != firstModel || qp.Effort != firstEffort || qp.ContextTier != firstTier || qp.ReplyChannelID != firstChannel || qp.MarkReady != prompts[0].MarkReady {
			// Different override or reply channel: end of the leading group. Keeping
			// reply channel in the grouping key ensures a group's response is routed
			// to exactly one channel (or none).
			break
		} else if firstOperationID != "" || qp.OperationID != firstOperationID {
			// Durable operation-ID prompts must execute independently so each
			// operation owns exactly one terminal transition. Legacy prompts
			// (empty IDs) retain override-group batching.
			break
		}
		ids = append(ids, id)
		prompts = append(prompts, qp)
		totalBytes += len(qp.Prompt)
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
	if len(prompts) == 0 {
		if _, err := tx.Exec(`UPDATE sessions SET status = ?, revision = revision + 1, updated_at = ? WHERE session_id = ?`, StateIdle, time.Now().UTC(), sessionID); err != nil {
			return nil, fmt.Errorf("mark drained session idle: %w", err)
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
	return s.CreateScheduleFull(sessionID, prompt, scheduledAt, "", 0, false)
}

// CreateScheduleWithCron adds a scheduled prompt with an optional cron expression for recurrence.
func (s *Store) CreateScheduleWithCron(sessionID, prompt string, scheduledAt time.Time, cronExpr string) (int64, error) {
	return s.CreateScheduleFull(sessionID, prompt, scheduledAt, cronExpr, 0, false)
}

// MarkScheduleResultReady records that a scheduled run completed with an
// explicitly requested ready notification.
func (s *Store) MarkScheduleResultReady(sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET schedule_ready_at = ?, revision = revision + 1 WHERE session_id = ?`, time.Now().UTC(), sessionID)
	if err != nil {
		return fmt.Errorf("mark scheduled result ready: %w", err)
	}
	return nil
}

// CreateScheduleFull adds a scheduled prompt with optional cron recurrence and an
// optional reply channel id (0 = none) whose output is delivered to that channel.
func (s *Store) CreateScheduleFull(sessionID, prompt string, scheduledAt time.Time, cronExpr string, replyChannelID int64, markReady bool) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM
		(SELECT 1 FROM schedules WHERE session_id = ? AND status IN (?, ?) LIMIT ?)`,
		sessionID, SchedulePending, ScheduleRunning, MaxActiveSchedules).Scan(&count); err != nil {
		return 0, err
	}
	if count >= MaxActiveSchedules {
		return 0, fmt.Errorf("session has reached the limit of %d active schedules; cancel unwanted schedules first", MaxActiveSchedules)
	}
	res, err := tx.Exec(
		`INSERT INTO schedules (session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, mark_ready) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, prompt, scheduledAt.UTC(), SchedulePending, cronExpr, replyChannelID, boolToInt(markReady),
	)
	if err != nil {
		return 0, fmt.Errorf("create schedule: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// GetSchedule retrieves a schedule by ID.
func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	row := s.db.QueryRow(
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, mark_ready, created_at FROM schedules WHERE id = ?`, id,
	)
	sch := &Schedule{}
	var markReady int
	err := row.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &markReady, &sch.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	sch.MarkReady = markReady != 0
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
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, mark_ready, created_at
			 FROM schedules WHERE session_id = ? AND status = ? ORDER BY scheduled_at`, sessionID, statusFilter,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, mark_ready, created_at
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
		var markReady int
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &markReady, &sch.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sch.MarkReady = markReady != 0
		schedules = append(schedules, sch)
	}
	return schedules, rows.Err()
}

// ListSchedulesPage returns a bounded page of schedules.
func (s *Store) ListSchedulesPage(ctx context.Context, sessionID, statusFilter string, limit, offset int) ([]*Schedule, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedules WHERE session_id = ? AND (? = '' OR status = ?)`, sessionID, statusFilter, statusFilter).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count schedules: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, session_id, prompt, scheduled_at, status, cron_expr, reply_channel_id, mark_ready, created_at
		 FROM schedules WHERE session_id = ? AND (? = '' OR status = ?)
		 ORDER BY scheduled_at, id LIMIT ? OFFSET ?`, sessionID, statusFilter, statusFilter, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list schedule page: %w", err)
	}
	defer rows.Close()
	var schedules []*Schedule
	for rows.Next() {
		sch := &Schedule{}
		var markReady int
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &markReady, &sch.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan schedule page: %w", err)
		}
		sch.MarkReady = markReady != 0
		schedules = append(schedules, sch)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return schedules, total, nil
}

// DueSchedules returns at most one due occurrence per session per tick, so a
// single session's backlog cannot crowd every other session out of the batch.
func (s *Store) DueSchedules(ctx context.Context) ([]*Schedule, error) {
	now := time.Now().UTC()
	rows, err := s.db.QueryContext(ctx,
		`SELECT sch.id, sch.session_id, sch.prompt, sch.scheduled_at, sch.status, sch.cron_expr, sch.reply_channel_id, sch.mark_ready, sch.created_at
		 FROM sessions sess JOIN schedules sch ON sch.id = (
			SELECT id FROM schedules WHERE session_id = sess.session_id AND status = ? AND scheduled_at <= ?
			ORDER BY scheduled_at, id LIMIT 1)
		 ORDER BY sch.scheduled_at, sch.id LIMIT ?`,
		SchedulePending, now, MaxScheduleBatch,
	)
	if err != nil {
		return nil, fmt.Errorf("due schedules: %w", err)
	}
	defer rows.Close()

	var schedules []*Schedule
	for rows.Next() {
		sch := &Schedule{}
		var markReady int
		if err := rows.Scan(&sch.ID, &sch.SessionID, &sch.Prompt, &sch.ScheduledAt, &sch.Status, &sch.CronExpr, &sch.ReplyChannelID, &markReady, &sch.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sch.MarkReady = markReady != 0
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM schedules WHERE id = ? AND status = ?`, id, SchedulePending)
	if err != nil {
		return fmt.Errorf("cancel schedule: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("schedule %d not found or not pending", id)
	}
	if _, err := tx.Exec(`DELETE FROM prompt_queue WHERE schedule_id = ? AND schedule_id != 0`, id); err != nil {
		return fmt.Errorf("cancel queued occurrence: %w", err)
	}
	return tx.Commit()
}

// UpdateSchedule edits the prompt, next-run time, cron expression, and reply
// channel of an existing pending schedule in place (preserving its ID). Only
// pending schedules can be edited. Returns an error if the schedule does not
// exist or is not pending.
func (s *Store) UpdateSchedule(id int64, prompt string, scheduledAt time.Time, cronExpr string, replyChannelID int64, markReady bool) error {
	res, err := s.db.Exec(
		`UPDATE schedules SET prompt = ?, scheduled_at = ?, cron_expr = ?, reply_channel_id = ?, mark_ready = ? WHERE id = ? AND status = ?`,
		prompt, scheduledAt.UTC(), cronExpr, replyChannelID, boolToInt(markReady), id, SchedulePending,
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
