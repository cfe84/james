package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"james/moneypenny/pkg/questionnaire"
)

var ErrQuestionnaireInvalid = errors.New("invalid questionnaire request")

func (s *Store) PublishQuestionnaire(sessionID string, definition questionnaire.Definition) error {
	content, err := json.Marshal(definition)
	if err != nil {
		return err
	}
	if len(content) > questionnaire.MaxDefinition {
		return fmt.Errorf("%w: questionnaire exceeds %d bytes", ErrQuestionnaireInvalid, questionnaire.MaxDefinition)
	}
	return s.addConversationTurn(sessionID, questionnaire.Role, string(content), "", "", func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM conversation_turns WHERE session_id = ? AND role = ?`,
			sessionID, questionnaire.Role).Scan(&count); err != nil {
			return err
		}
		if count >= questionnaire.MaxPending {
			return fmt.Errorf("%w: at most %d questionnaires can be pending", ErrQuestionnaireInvalid, questionnaire.MaxPending)
		}
		return nil
	})
}

func (s *Store) PendingQuestionnaires(sessionID string) ([]questionnaire.Card, error) {
	rows, err := s.db.Query(`SELECT id, content FROM conversation_turns WHERE session_id = ? AND role = ? ORDER BY id`,
		sessionID, questionnaire.Role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := []questionnaire.Card{}
	for rows.Next() {
		var card questionnaire.Card
		var content string
		if err := rows.Scan(&card.ID, &content); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(content), &card.Definition); err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	return cards, rows.Err()
}

// ResolveQuestionnaire closes the card and enqueues its reply in one transaction.
// The return value reports whether this call claimed the idle queue runner.
func (s *Store) ResolveQuestionnaire(r questionnaire.Resolution) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var content, role, status string
	err = tx.QueryRow(`SELECT t.content, t.role, s.status FROM conversation_turns t
		JOIN sessions s ON s.session_id = t.session_id WHERE t.id = ? AND t.session_id = ?
		AND t.role IN (?, ?, ?)`, r.ID, r.SessionID, questionnaire.Role,
		questionnaire.SubmittedRole, questionnaire.DiscardedRole).Scan(&content, &role, &status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && role != questionnaire.Role) {
		return false, fmt.Errorf("%w: questionnaire is not pending in this session", ErrQuestionnaireInvalid)
	}
	if err != nil {
		return false, err
	}
	claimed := false
	switch r.Action {
	case "submit":
		var definition questionnaire.Definition
		if err := json.Unmarshal([]byte(content), &definition); err != nil {
			return false, err
		}
		prompt, err := definition.Reply(r.Answers)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrQuestionnaireInvalid, err)
		}
		if _, err := tx.Exec(`INSERT INTO prompt_queue (session_id, prompt, source) VALUES (?, ?, ?)`,
			r.SessionID, prompt, questionnaire.Role); err != nil {
			return false, err
		}
		role = questionnaire.SubmittedRole
		claimed = status != StateWorking
		status = StateWorking
	case "discard":
		if len(r.Answers) != 0 {
			return false, fmt.Errorf("%w: discard does not accept answers", ErrQuestionnaireInvalid)
		}
		role = questionnaire.DiscardedRole
	default:
		return false, fmt.Errorf("%w: action must be submit or discard", ErrQuestionnaireInvalid)
	}
	if _, err := tx.Exec(`UPDATE conversation_turns SET role = ? WHERE id = ?`, role, r.ID); err != nil {
		return false, err
	}
	var revision, generation int64
	if err := tx.QueryRow(`UPDATE sessions SET status = ?, revision = revision + 1, updated_at = ?
		WHERE session_id = ? RETURNING revision, generation`, status, time.Now().UTC(), r.SessionID).Scan(&revision, &generation); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.notifyConversationTurn(r.SessionID, role, content, "", "", r.ID, revision, generation)
	return claimed, nil
}
