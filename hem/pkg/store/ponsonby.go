package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"james/moneypenny/pkg/envelope"
)

type Ponsonby struct {
	envelope.PonsonbyStore
	Moneypenny string `json:"moneypenny"`
	Default    bool   `json:"default"`
}

func (s *Store) ListPonsonbies() ([]Ponsonby, error) {
	rows, err := s.db.Query("SELECT definition FROM ponsonbies ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ponsonby{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var p Ponsonby
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SavePonsonby(p Ponsonby) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ponsonbies(id,name,definition) VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,definition=excluded.definition`, p.ID, p.Name, string(raw))
	return err
}

func (s *Store) SetPonsonbies(sessionID string, attachments []envelope.Ponsonby) error {
	if attachments == nil {
		attachments = []envelope.Ponsonby{}
	}
	raw, err := json.Marshal(attachments)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO session_ponsonbies(session_id,attachments) VALUES(?,?)
		ON CONFLICT(session_id) DO UPDATE SET attachments=excluded.attachments`, sessionID, string(raw))
	return err
}

func (s *Store) SessionPonsonbies(sessionID string) ([]envelope.Ponsonby, error) {
	var raw string
	err := s.db.QueryRow("SELECT attachments FROM session_ponsonbies WHERE session_id=?", sessionID).Scan(&raw)
	if err != nil {
		// Old sessions have no assignments; defaults apply only at creation.
		if err == sql.ErrNoRows {
			return []envelope.Ponsonby{}, nil
		}

		return nil, err
	}
	var out []envelope.Ponsonby
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode Ponsonby assignments: %w", err)
	}
	return out, nil
}

func (s *Store) PonsonbyAssigned(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM session_ponsonbies,json_each(attachments) a WHERE json_extract(a.value,'$.id')=?`, id).Scan(&n)
	return n > 0, err
}

func (s *Store) DeletePonsonby(id string) error {
	attached, err := s.PonsonbyAssigned(id)
	if err != nil {
		return err
	}
	if attached {
		return fmt.Errorf("remove store assignments from sessions before deleting it")
	}
	_, err = s.db.Exec("DELETE FROM ponsonbies WHERE id=?", id)
	return err
}
