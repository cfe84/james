package store

import (
	"database/sql"
	"fmt"
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
	CreatedAt     time.Time
}

// Session represents a tracked session (mapping session to moneypenny).
type Session struct {
	SessionID      string
	MoneypennyName string
	CreatedAt      time.Time
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
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
    session_id TEXT PRIMARY KEY,
    moneypenny_name TEXT NOT NULL REFERENCES moneypennies(name) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// AddMoneypenny inserts a new moneypenny. Returns an error if the name already exists.
func (s *Store) AddMoneypenny(mp *Moneypenny) error {
	_, err := s.db.Exec(
		`INSERT INTO moneypennies (name, transport_type, fifo_in, fifo_out, mi6_addr, is_default)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		mp.Name, mp.TransportType, mp.FIFOIn, mp.FIFOOut, mp.MI6Addr, boolToInt(mp.IsDefault),
	)
	if err != nil {
		return fmt.Errorf("add moneypenny %q: %w", mp.Name, err)
	}
	return nil
}

// GetMoneypenny retrieves a moneypenny by name. Returns nil, nil if not found.
func (s *Store) GetMoneypenny(name string) (*Moneypenny, error) {
	row := s.db.QueryRow(
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, created_at
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
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, created_at
		 FROM moneypennies ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list moneypennies: %w", err)
	}
	defer rows.Close()

	var result []*Moneypenny
	for rows.Next() {
		var mp Moneypenny
		var isDefault int
		if err := rows.Scan(&mp.Name, &mp.TransportType, &mp.FIFOIn, &mp.FIFOOut, &mp.MI6Addr, &isDefault, &mp.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan moneypenny: %w", err)
		}
		mp.IsDefault = isDefault != 0
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
		`SELECT name, transport_type, fifo_in, fifo_out, mi6_addr, is_default, created_at
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
func (s *Store) TrackSession(sessionID, moneypennyName string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, moneypenny_name) VALUES (?, ?)`,
		sessionID, moneypennyName,
	)
	if err != nil {
		return fmt.Errorf("track session %q: %w", sessionID, err)
	}
	return nil
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
			`SELECT session_id, moneypenny_name, created_at FROM sessions ORDER BY session_id`,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT session_id, moneypenny_name, created_at FROM sessions WHERE moneypenny_name = ? ORDER BY session_id`,
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
		if err := rows.Scan(&sess.SessionID, &sess.MoneypennyName, &sess.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, &sess)
	}
	return result, rows.Err()
}

// DeleteTrackedSession removes a tracked session by ID.
func (s *Store) DeleteTrackedSession(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete session %q: %w", sessionID, err)
	}
	return nil
}

// scanMoneypenny scans a single row into a Moneypenny.
func scanMoneypenny(row *sql.Row) (*Moneypenny, error) {
	var mp Moneypenny
	var isDefault int
	err := row.Scan(&mp.Name, &mp.TransportType, &mp.FIFOIn, &mp.FIFOOut, &mp.MI6Addr, &isDefault, &mp.CreatedAt)
	if err != nil {
		return nil, err
	}
	mp.IsDefault = isDefault != 0
	return &mp, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
