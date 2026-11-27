package envelope

import (
	"fmt"
	"strings"
	"time"
)

type ConversationQuery struct {
	SessionID  string `json:"session_id,omitempty"`
	Text       string `json:"text,omitempty"`
	Role       string `json:"role,omitempty"`
	Order      string `json:"order,omitempty"`
	After      string `json:"after,omitempty"`
	Before     string `json:"before,omitempty"`
	AfterTurn  int64  `json:"after_turn,omitempty"`
	BeforeTurn int64  `json:"before_turn,omitempty"`
	Turn       int64  `json:"turn,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
}

func (q *ConversationQuery) Normalize(search bool) error {
	q.Text = strings.TrimSpace(q.Text)
	if (search && (q.Text == "" || len(q.Text) > 512 || q.Turn != 0)) ||
		(!search && q.Text != "") {
		return fmt.Errorf("search requires 1–512 bytes of text; --turn is read-only")
	}
	if q.Role != "" && q.Role != "user" && q.Role != "assistant" {
		return fmt.Errorf("role must be user or assistant")
	}
	if q.Order == "" {
		q.Order = "newest"
	}
	if q.Order != "newest" && q.Order != "oldest" {
		return fmt.Errorf("order must be newest or oldest")
	}
	if q.Limit < 0 || q.Limit > 50 || q.Turn < 0 || q.AfterTurn < 0 || q.BeforeTurn < 0 || len(q.Cursor) > 512 {
		return fmt.Errorf("invalid limit, turn ID, or cursor")
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	for _, field := range []*string{&q.After, &q.Before} {
		if *field != "" {
			at, err := time.Parse(time.RFC3339Nano, *field)
			if err != nil {
				return fmt.Errorf("after and before must be RFC3339 timestamps")
			}
			*field = at.UTC().Format("2006-01-02 15:04:05.999999999")
		}
	}
	return nil
}

type ConversationMatch struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	Content   string `json:"content"`
	Offset    int64  `json:"offset,omitempty"`
	Partial   bool   `json:"partial,omitempty"`
}

type ConversationPage struct {
	SessionID  string              `json:"session_id"`
	Turns      []ConversationMatch `json:"turns"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

// RoutedConversationQuery is sent only over the trusted operator transport.
type RoutedConversationQuery struct {
	Caller string            `json:"caller"`
	Search bool              `json:"search"`
	Query  ConversationQuery `json:"query"`
}
