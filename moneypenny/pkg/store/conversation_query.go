package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"james/moneypenny/pkg/envelope"
)

const conversationReadBytes = 4096

type conversationCursor struct {
	Version    int
	Scope      [32]byte
	Generation int64
	MaxID      int64
	LastID     int64
	Offset     int64
}

func (s *Store) QueryConversation(ctx context.Context, q envelope.ConversationQuery, search bool, caller string) (envelope.ConversationPage, error) {
	page := envelope.ConversationPage{SessionID: q.SessionID, Turns: []envelope.ConversationMatch{}}
	if err := q.Normalize(search); err != nil {
		return page, err
	}
	token := q.Cursor
	q.Cursor = ""
	scope, err := json.Marshal([]any{caller, search, q})
	if err != nil {
		return page, err
	}
	digest := sha256.Sum256(scope)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return page, err
	}
	defer conn.Close()
	// The store defaults to BEGIN IMMEDIATE for writers. A search must not reserve the write lock.
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return page, err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	c := conversationCursor{Version: 1, Scope: digest}
	if err := conn.QueryRowContext(ctx, `SELECT generation, COALESCE(
		(SELECT MAX(id) FROM conversation_turns WHERE session_id = sessions.session_id AND role IN ('user', 'assistant')), 0)
		FROM sessions WHERE session_id = ?`, q.SessionID).Scan(&c.Generation, &c.MaxID); err != nil {
		return page, fmt.Errorf("looking up conversation: %w", err)
	}
	if token != "" {
		var saved conversationCursor
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil || json.Unmarshal(raw, &saved) != nil || saved.Version != c.Version || saved.Scope != digest ||
			saved.Generation != c.Generation || saved.MaxID > c.MaxID || saved.LastID <= 0 ||
			saved.LastID > saved.MaxID || saved.Offset < 0 || (search && saved.Offset != 0) {
			return page, errors.New("invalid or stale conversation cursor; restart with the original filters")
		}
		c = saved
	}

	where := []string{"session_id = ?", "id <= ?", "role IN ('user', 'assistant')"}
	args := []any{q.SessionID, c.MaxID}
	add := func(expr string, value any) {
		where, args = append(where, expr), append(args, value)
	}
	if q.Role != "" {
		add("role = ?", q.Role)
	}
	if q.Turn != 0 {
		add("id = ?", q.Turn)
	}
	if q.After != "" {
		add("created_at > ?", q.After)
	}
	if q.Before != "" {
		add("created_at < ?", q.Before)
	}
	if q.AfterTurn != 0 {
		add("id > ?", q.AfterTurn)
	}
	if q.BeforeTurn != 0 {
		add("id < ?", q.BeforeTurn)
	}
	order, cmp := "DESC", "<"
	if q.Order == "oldest" {
		order, cmp = "ASC", ">"
	}
	if c.LastID != 0 {
		if c.Offset > 0 {
			cmp += "="
		}
		add("id "+cmp+" ?", c.LastID)
	}
	content := `substr(CAST(content AS BLOB), CASE WHEN id = ? THEN ? + 1 ELSE 1 END, ?)`
	selectArgs := []any{c.LastID, c.Offset, conversationReadBytes + utf8.UTFMax}
	if search {
		add("instr(lower(content), lower(?)) > 0", q.Text)
		content = `substr(content, max(1, instr(lower(content), lower(?)) - 80), 320)`
		selectArgs = []any{q.Text}
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, role, created_at, `+content+
		` FROM conversation_turns WHERE `+strings.Join(where, " AND ")+` ORDER BY id `+order+` LIMIT ?`,
		append(append(selectArgs, args...), q.Limit+1)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	more := false
	for rows.Next() {
		if len(page.Turns) == q.Limit {
			more = true
			break
		}
		var turn envelope.ConversationMatch
		var at time.Time
		if err := rows.Scan(&turn.ID, &turn.Role, &at, &turn.Content); err != nil {
			return page, err
		}
		turn.CreatedAt = at.UTC().Format(time.RFC3339Nano)
		if turn.ID == c.LastID {
			turn.Offset = c.Offset
		}
		c.LastID, c.Offset = turn.ID, 0
		if !search && len(turn.Content) > conversationReadBytes {
			end := conversationReadBytes
			for end > 0 && !utf8.RuneStart(turn.Content[end]) {
				end--
			}
			if end == 0 {
				end = conversationReadBytes
			}
			turn.Content, turn.Partial = turn.Content[:end], true
			c.Offset, more = turn.Offset+int64(end), true
		}
		page.Turns = append(page.Turns, turn)
		if turn.Partial {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if more {
		raw, err := json.Marshal(c)
		if err != nil {
			return page, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
