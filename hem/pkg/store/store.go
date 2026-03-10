package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TransportType indicates how to reach a moneypenny.
const (
	TransportFIFO = "fifo"
	TransportMI6  = "mi6"
)

// Moneypenny represents a registered moneypenny instance.
type Moneypenny struct {
	Name          string
	TransportType string // "fifo" or "mi6"
	FIFOIn        string // path to input FIFO (for fifo transport)
	FIFOOut       string // path to output FIFO (for fifo transport)
	MI6Addr       string // mi6 address host/session_id (for mi6 transport)
	IsDefault     bool
	Enabled       bool // false = disabled, hidden from dashboard/sessions
	CreatedAt     time.Time
}

// Session represents a tracked session (mapping session to moneypenny).
type Session struct {
	SessionID       string
	MoneypennyName  string
	ProjectID       string
	ParentSessionID string // non-empty for sub-sessions
	HemStatus       string // "active" or "completed"
	Reviewed        bool   // true if user has seen latest response
	CreatedAt       time.Time
}

// Project represents a project that groups sessions.
type Project struct {
	ID                  string
	Name                string
	Status              string // active, paused, done
	Moneypenny          string
	Paths               string // JSON array
	DefaultAgent        string
	DefaultSystemPrompt string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// AgentTemplate is a reusable session template attached to a project.
type AgentTemplate struct {
	ID           string
	ProjectID    string
	Name         string
	Agent        string
	Path         string
	SystemPrompt string
	Prompt       string
	Yolo         bool
	CreatedAt    time.Time
}

// Store manages Hem's SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at dbPath and initialises the schema.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Enable WAL mode.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Create tables.
	schema := `
CREATE TABLE IF NOT EXISTS moneypennies (
    name TEXT PRIMARY KEY,
    transport_type TEXT NOT NULL,
    fifo_in TEXT NOT NULL DEFAULT '',
    fifo_out TEXT NOT NULL DEFAULT '',
    mi6_addr TEXT NOT NULL DEFAULT '',
    is_default INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    moneypenny TEXT NOT NULL DEFAULT '',
    paths TEXT NOT NULL DEFAULT '[]',
    default_agent TEXT NOT NULL DEFAULT 'claude',
    default_system_prompt TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    moneypenny_name TEXT NOT NULL REFERENCES moneypennies(name) ON DELETE CASCADE,
    project_id TEXT NOT NULL DEFAULT '',
    hem_status TEXT NOT NULL DEFAULT 'active',
    reviewed INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS defaults (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_templates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    agent TEXT NOT NULL DEFAULT 'claude',
    path TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    yolo INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	s := &Store{db: db}

	// Run migrations for existing databases.
	if err := s.migrateSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddMoneypenny inserts a new moneypenny. Returns an error if the name already exists.
func (s *Store) AddMoneypenny(mp *Moneypenny) error {
	_, err := s.db.Exec(
		`INSERT INTO moneypennies (name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mp.Name, mp.TransportType, mp.FIFOIn, mp.FIFOOut, mp.MI6Addr, boolToInt(mp.IsDefault), boolToInt(mp.Enabled),
	)
	if err != nil {
		return fmt.Errorf("add moneypenny %q: %w", mp.Name, err)
	}
	return nil
}

// GetMoneypenny retrieves a moneypenny by name. Returns nil, nil if not found.
func (s *Store) GetMoneypenny(name string) (*Moneypenny, error) {
	row := s.db.QueryRow(
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, enabled, created_at
		 FROM moneypennies WHERE name = ?`, name,
	)
	mp, err := scanMoneypenny(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get moneypenny %q: %w", name, err)
	}
	return mp, nil
}

// ListMoneypennies returns all registered moneypennies ordered by name.
func (s *Store) ListMoneypennies() ([]*Moneypenny, error) {
	rows, err := s.db.Query(
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, enabled, created_at
		 FROM moneypennies ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list moneypennies: %w", err)
	}
	defer rows.Close()

	var result []*Moneypenny
	for rows.Next() {
		var mp Moneypenny
		var isDefault, enabled int
		if err := rows.Scan(&mp.Name, &mp.TransportType, &mp.FIFOIn, &mp.FIFOOut, &mp.MI6Addr, &isDefault, &enabled, &mp.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan moneypenny: %w", err)
		}
		mp.IsDefault = isDefault != 0
		mp.Enabled = enabled != 0
		result = append(result, &mp)
	}
	return result, rows.Err()
}

// DeleteMoneypenny removes a moneypenny by name.
func (s *Store) DeleteMoneypenny(name string) error {
	_, err := s.db.Exec(`DELETE FROM moneypennies WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete moneypenny %q: %w", name, err)
	}
	return nil
}

// SetDefaultMoneypenny clears the current default and sets the given name as default.
// Returns an error if the name does not exist.
func (s *Store) SetDefaultMoneypenny(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Verify the moneypenny exists.
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM moneypennies WHERE name = ?`, name).Scan(&exists); err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("moneypenny %q not found", name)
	}

	// Clear all defaults.
	if _, err := tx.Exec(`UPDATE moneypennies SET is_default = 0`); err != nil {
		return fmt.Errorf("clear defaults: %w", err)
	}

	// Set the new default.
	if _, err := tx.Exec(`UPDATE moneypennies SET is_default = 1 WHERE name = ?`, name); err != nil {
		return fmt.Errorf("set default: %w", err)
	}

	return tx.Commit()
}

// GetDefaultMoneypenny returns the current default moneypenny. Returns nil, nil if none is set.
func (s *Store) GetDefaultMoneypenny() (*Moneypenny, error) {
	row := s.db.QueryRow(
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, enabled, created_at
		 FROM moneypennies WHERE is_default = 1`,
	)
	mp, err := scanMoneypenny(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get default moneypenny: %w", err)
	}
	return mp, nil
}

// TrackSession records that a session is associated with a moneypenny.
// projectID may be empty.
func (s *Store) TrackSession(sessionID, moneypennyName string, projectID ...string) error {
	pid := ""
	if len(projectID) > 0 {
		pid = projectID[0]
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed) VALUES (?, ?, ?, '', 'active', 0)`,
		sessionID, moneypennyName, pid,
	)
	if err != nil {
		return fmt.Errorf("track session %q: %w", sessionID, err)
	}
	return nil
}

// TrackSessionIfNew tracks a session only if it doesn't already exist in the store.
// Returns true if the session was newly inserted.
// Used by sync to adopt sessions from moneypennies without overwriting existing tracking data.
func (s *Store) TrackSessionIfNew(sessionID, moneypennyName string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO sessions (session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed) VALUES (?, ?, '', '', 'active', 0)`,
		sessionID, moneypennyName,
	)
	if err != nil {
		return false, fmt.Errorf("track session if new %q: %w", sessionID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetSessionMoneypenny returns the moneypenny name for a session. Returns "" if not found.
func (s *Store) GetSessionMoneypenny(sessionID string) (string, error) {
	var name string
	err := s.db.QueryRow(
		`SELECT moneypenny_name FROM sessions WHERE session_id = ?`, sessionID,
	).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get session moneypenny %q: %w", sessionID, err)
	}
	return name, nil
}

// ListTrackedSessions returns tracked sessions. If moneypennyFilter is non-empty,
// only sessions for that moneypenny are returned.
func (s *Store) ListTrackedSessions(moneypennyFilter string) ([]*Session, error) {
	var rows *sql.Rows
	var err error
	if moneypennyFilter == "" {
		rows, err = s.db.Query(
			`SELECT session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed, created_at FROM sessions ORDER BY session_id`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed, created_at FROM sessions WHERE moneypenny_name = ? ORDER BY session_id`,
			moneypennyFilter,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []*Session
	for rows.Next() {
		var sess Session
		var reviewed int
		if err := rows.Scan(&sess.SessionID, &sess.MoneypennyName, &sess.ProjectID, &sess.ParentSessionID, &sess.HemStatus, &reviewed, &sess.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sess.Reviewed = reviewed != 0
		result = append(result, &sess)
	}
	return result, rows.Err()
}

// TrackSubSession records a sub-session with a parent link.
func (s *Store) TrackSubSession(sessionID, moneypennyName, parentSessionID string, projectID ...string) error {
	pid := ""
	if len(projectID) > 0 {
		pid = projectID[0]
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed) VALUES (?, ?, ?, ?, 'active', 0)`,
		sessionID, moneypennyName, pid, parentSessionID,
	)
	if err != nil {
		return fmt.Errorf("track sub-session %q: %w", sessionID, err)
	}
	return nil
}

// ListSubSessions returns all sub-sessions for a given parent session.
func (s *Store) ListSubSessions(parentSessionID string) ([]*Session, error) {
	rows, err := s.db.Query(
		`SELECT session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed, created_at FROM sessions WHERE parent_session_id = ? ORDER BY created_at`,
		parentSessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("list sub-sessions: %w", err)
	}
	defer rows.Close()

	var result []*Session
	for rows.Next() {
		var sess Session
		var reviewed int
		if err := rows.Scan(&sess.SessionID, &sess.MoneypennyName, &sess.ProjectID, &sess.ParentSessionID, &sess.HemStatus, &reviewed, &sess.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan sub-session: %w", err)
		}
		sess.Reviewed = reviewed != 0
		result = append(result, &sess)
	}
	return result, rows.Err()
}

// GetSession returns a tracked session by ID. Returns nil, nil if not found.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	var sess Session
	var reviewed int
	err := s.db.QueryRow(
		`SELECT session_id, moneypenny_name, project_id, parent_session_id, hem_status, reviewed, created_at FROM sessions WHERE session_id = ?`,
		sessionID,
	).Scan(&sess.SessionID, &sess.MoneypennyName, &sess.ProjectID, &sess.ParentSessionID, &sess.HemStatus, &reviewed, &sess.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session %q: %w", sessionID, err)
	}
	sess.Reviewed = reviewed != 0
	return &sess, nil
}

// DeleteTrackedSession removes a tracked session by ID, including all sub-sessions.
func (s *Store) DeleteTrackedSession(sessionID string) error {
	// Cascade: delete sub-sessions first.
	_, err := s.db.Exec(`DELETE FROM sessions WHERE parent_session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete sub-sessions of %q: %w", sessionID, err)
	}
	_, err = s.db.Exec(`DELETE FROM sessions WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}
	return nil
}

// scanMoneypenny scans a single row into a Moneypenny.
func scanMoneypenny(row *sql.Row) (*Moneypenny, error) {
	var mp Moneypenny
	var isDefault, enabled int
	err := row.Scan(&mp.Name, &mp.TransportType, &mp.FIFOIn, &mp.FIFOOut, &mp.MI6Addr, &isDefault, &enabled, &mp.CreatedAt)
	if err != nil {
		return nil, err
	}
	mp.IsDefault = isDefault != 0
	mp.Enabled = enabled != 0
	return &mp, nil
}

// SetMoneypennyEnabled sets the enabled flag on a moneypenny.
func (s *Store) SetMoneypennyEnabled(name string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE moneypennies SET enabled = ? WHERE name = ?`, boolToInt(enabled), name)
	if err != nil {
		return fmt.Errorf("set moneypenny enabled %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("moneypenny %q not found", name)
	}
	return nil
}

// DisabledMoneypennyNames returns the set of disabled moneypenny names.
func (s *Store) DisabledMoneypennyNames() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT name FROM moneypennies WHERE enabled = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

// SetDefault sets a default value by key.
func (s *Store) SetDefault(key, value string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO defaults (key, value) VALUES (?, ?)`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set default %q: %w", key, err)
	}
	return nil
}

// GetDefault returns a default value by key. Returns "" if not set.
func (s *Store) GetDefault(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM defaults WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get default %q: %w", key, err)
	}
	return value, nil
}

// DeleteDefault removes a default value by key.
func (s *Store) DeleteDefault(key string) error {
	_, err := s.db.Exec(`DELETE FROM defaults WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete default %q: %w", key, err)
	}
	return nil
}

// ListDefaults returns all defaults as key-value pairs.
func (s *Store) ListDefaults() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM defaults ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list defaults: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan default: %w", err)
		}
		result[k] = v
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------------------
// Project methods
// ---------------------------------------------------------------------------

// CreateProject inserts a new project.
func (s *Store) CreateProject(p *Project) error {
	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, status, moneypenny, paths, default_agent, default_system_prompt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Status, p.Moneypenny, p.Paths, p.DefaultAgent, p.DefaultSystemPrompt, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create project %q: %w", p.Name, err)
	}
	return nil
}

// GetProject retrieves a project by ID first, then by name. Returns nil, nil if not found.
func (s *Store) GetProject(nameOrID string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT id, name, status, moneypenny, paths, default_agent, default_system_prompt, created_at, updated_at
		 FROM projects WHERE id = ?`, nameOrID,
	)
	p, err := scanProject(row)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("get project %q: %w", nameOrID, err)
	}

	// Try by name.
	row = s.db.QueryRow(
		`SELECT id, name, status, moneypenny, paths, default_agent, default_system_prompt, created_at, updated_at
		 FROM projects WHERE name = ?`, nameOrID,
	)
	p, err = scanProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project %q: %w", nameOrID, err)
	}
	return p, nil
}

// ListProjects returns projects, optionally filtered by status.
func (s *Store) ListProjects(statusFilter string) ([]*Project, error) {
	var rows *sql.Rows
	var err error
	if statusFilter == "" {
		rows, err = s.db.Query(
			`SELECT id, name, status, moneypenny, paths, default_agent, default_system_prompt, created_at, updated_at
			 FROM projects ORDER BY name`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, status, moneypenny, paths, default_agent, default_system_prompt, created_at, updated_at
			 FROM projects WHERE status = ? ORDER BY name`, statusFilter,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var result []*Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Status, &p.Moneypenny, &p.Paths, &p.DefaultAgent, &p.DefaultSystemPrompt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		result = append(result, &p)
	}
	return result, rows.Err()
}

// UpdateProject updates specified fields of a project. Only non-nil pointer fields are updated.
func (s *Store) UpdateProject(id string, name, status, moneypenny, paths, defaultAgent, defaultSystemPrompt *string) error {
	var sets []string
	var args []interface{}

	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *status)
	}
	if moneypenny != nil {
		sets = append(sets, "moneypenny = ?")
		args = append(args, *moneypenny)
	}
	if paths != nil {
		sets = append(sets, "paths = ?")
		args = append(args, *paths)
	}
	if defaultAgent != nil {
		sets = append(sets, "default_agent = ?")
		args = append(args, *defaultAgent)
	}
	if defaultSystemPrompt != nil {
		sets = append(sets, "default_system_prompt = ?")
		args = append(args, *defaultSystemPrompt)
	}

	if len(sets) == 0 {
		return nil
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now())
	args = append(args, id)

	query := fmt.Sprintf("UPDATE projects SET %s WHERE id = ?", strings.Join(sets, ", "))
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update project %q: %w", id, err)
	}
	return nil
}

// DeleteProject removes a project by id.
func (s *Store) DeleteProject(id string) error {
	// Clear project_id on sessions that reference this project.
	if _, err := s.db.Exec(`UPDATE sessions SET project_id = '' WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("unlinking sessions from project %q: %w", id, err)
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project %q: %w", id, err)
	}
	return nil
}

// SetSessionProject updates the project_id on a session.
func (s *Store) SetSessionProject(sessionID, projectID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET project_id = ? WHERE session_id = ?`, projectID, sessionID)
	if err != nil {
		return fmt.Errorf("set session project %q: %w", sessionID, err)
	}
	return nil
}

// SetSessionHemStatus updates the hem_status on a session.
func (s *Store) SetSessionHemStatus(sessionID, hemStatus string) error {
	_, err := s.db.Exec(`UPDATE sessions SET hem_status = ? WHERE session_id = ?`, hemStatus, sessionID)
	if err != nil {
		return fmt.Errorf("set session hem_status %q: %w", sessionID, err)
	}
	return nil
}

// SetSessionReviewed updates the reviewed flag on a session.
func (s *Store) SetSessionReviewed(sessionID string, reviewed bool) error {
	_, err := s.db.Exec(`UPDATE sessions SET reviewed = ? WHERE session_id = ?`, boolToInt(reviewed), sessionID)
	if err != nil {
		return fmt.Errorf("set session reviewed %q: %w", sessionID, err)
	}
	return nil
}

// GetSessionHemStatus returns the hem_status for a session. Returns "" if not found.
func (s *Store) GetSessionHemStatus(sessionID string) (string, error) {
	var status string
	err := s.db.QueryRow(`SELECT hem_status FROM sessions WHERE session_id = ?`, sessionID).Scan(&status)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get session hem_status %q: %w", sessionID, err)
	}
	return status, nil
}

// scanProject scans a single row into a Project.
func scanProject(row *sql.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Status, &p.Moneypenny, &p.Paths, &p.DefaultAgent, &p.DefaultSystemPrompt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// AgentTemplate methods
// ---------------------------------------------------------------------------

// CreateTemplate inserts a new agent template.
func (s *Store) CreateTemplate(t *AgentTemplate) error {
	_, err := s.db.Exec(
		`INSERT INTO agent_templates (id, project_id, name, agent, path, system_prompt, prompt, yolo, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ProjectID, t.Name, t.Agent, t.Path, t.SystemPrompt, t.Prompt, boolToInt(t.Yolo), t.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("create template %q: %w", t.Name, err)
	}
	return nil
}

// GetTemplate retrieves a template by ID first, then by name within a project.
func (s *Store) GetTemplate(nameOrID string, projectID string) (*AgentTemplate, error) {
	var t AgentTemplate
	var yolo int
	err := s.db.QueryRow(
		`SELECT id, project_id, name, agent, path, system_prompt, prompt, yolo, created_at
		 FROM agent_templates WHERE id = ?`, nameOrID,
	).Scan(&t.ID, &t.ProjectID, &t.Name, &t.Agent, &t.Path, &t.SystemPrompt, &t.Prompt, &yolo, &t.CreatedAt)
	if err == nil {
		t.Yolo = yolo != 0
		return &t, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("get template %q: %w", nameOrID, err)
	}
	// Try by name within project.
	err = s.db.QueryRow(
		`SELECT id, project_id, name, agent, path, system_prompt, prompt, yolo, created_at
		 FROM agent_templates WHERE name = ? AND project_id = ?`, nameOrID, projectID,
	).Scan(&t.ID, &t.ProjectID, &t.Name, &t.Agent, &t.Path, &t.SystemPrompt, &t.Prompt, &yolo, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get template %q: %w", nameOrID, err)
	}
	t.Yolo = yolo != 0
	return &t, nil
}

// ListTemplates returns all templates for a project.
func (s *Store) ListTemplates(projectID string) ([]*AgentTemplate, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, agent, path, system_prompt, prompt, yolo, created_at
		 FROM agent_templates WHERE project_id = ? ORDER BY name`, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	defer rows.Close()
	var result []*AgentTemplate
	for rows.Next() {
		var t AgentTemplate
		var yolo int
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Agent, &t.Path, &t.SystemPrompt, &t.Prompt, &yolo, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		t.Yolo = yolo != 0
		result = append(result, &t)
	}
	return result, rows.Err()
}

// ListAllTemplates returns all templates across all projects, with project name.
func (s *Store) ListAllTemplates() ([]*AgentTemplate, map[string]string, error) {
	rows, err := s.db.Query(
		`SELECT t.id, t.project_id, t.name, t.agent, t.path, t.system_prompt, t.prompt, t.yolo, t.created_at, p.name
		 FROM agent_templates t
		 JOIN projects p ON t.project_id = p.id
		 ORDER BY p.name, t.name`,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list all templates: %w", err)
	}
	defer rows.Close()
	var result []*AgentTemplate
	projectNames := make(map[string]string) // template ID → project name
	for rows.Next() {
		var t AgentTemplate
		var yolo int
		var projName string
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Agent, &t.Path, &t.SystemPrompt, &t.Prompt, &yolo, &t.CreatedAt, &projName); err != nil {
			return nil, nil, fmt.Errorf("scan template: %w", err)
		}
		t.Yolo = yolo != 0
		result = append(result, &t)
		projectNames[t.ID] = projName
	}
	return result, projectNames, rows.Err()
}

// DeleteTemplate removes a template by ID.
func (s *Store) DeleteTemplate(id string) error {
	_, err := s.db.Exec(`DELETE FROM agent_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete template %q: %w", id, err)
	}
	return nil
}

// migrateSchema runs ALTER TABLE statements for existing databases,
// ignoring "duplicate column name" errors.
func (s *Store) migrateSchema() error {
	migrations := []string{
		`ALTER TABLE sessions ADD COLUMN project_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN hem_status TEXT NOT NULL DEFAULT 'active'`,
		`ALTER TABLE sessions ADD COLUMN reviewed INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE agent_templates ADD COLUMN yolo INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE sessions ADD COLUMN parent_session_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE moneypennies ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`,
	}
	for _, m := range migrations {
		_, err := s.db.Exec(m)
		if err != nil {
			// Ignore "duplicate column name" errors.
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
