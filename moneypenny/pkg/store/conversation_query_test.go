package store

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"james/moneypenny/pkg/envelope"
)

func TestConversationQuery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	long := strings.Repeat("x", 4095) + "é" + strings.Repeat("z", 500) + " Decision Made"
	base := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	turn := func(role, body string, offset time.Duration) *ConversationTurn {
		return &ConversationTurn{Role: role, Content: body, CreatedAt: base.Add(offset)}
	}
	if err := s.CreateSessionWithTurns(&Session{SessionID: "source", Agent: "copilot"}, "", []*ConversationTurn{
		turn("user", "first", 0), turn("thinking", "decision made", time.Second),
		turn("assistant", long, 2*time.Second), turn("user", "last", 3*time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	input := envelope.ConversationQuery{SessionID: "source", Text: "decision made", After: base.Add(time.Second).Format(time.RFC3339), Before: base.Add(3 * time.Second).Format(time.RFC3339)}
	matches, err := s.QueryConversation(ctx, input, true, "source")
	if err != nil || len(matches.Turns) != 1 || !strings.Contains(matches.Turns[0].Content, "Decision Made") {
		t.Fatalf("unexpected search: %+v, %v", matches, err)
	}
	input.After, input.Before = "", ""
	matches, err = s.QueryConversation(ctx, input, true, "source")
	if err != nil || len(matches.Turns) != 1 || matches.Turns[0].Role != "assistant" {
		t.Fatalf("search exposed a hidden role: %+v, %v", matches, err)
	}

	q := envelope.ConversationQuery{SessionID: "source", Turn: matches.Turns[0].ID}
	first, err := s.QueryConversation(ctx, q, false, "source")
	if err != nil || len(first.Turns) != 1 || first.NextCursor == "" || !first.Turns[0].Partial || !utf8.ValidString(first.Turns[0].Content) {
		t.Fatalf("read did not return a bounded UTF-8 fragment: %+v, %v", first, err)
	}
	q.Cursor = first.NextCursor
	rest, err := s.QueryConversation(ctx, q, false, "source")
	if err != nil || len(rest.Turns) != 1 || rest.NextCursor != "" || rest.Turns[0].Offset != int64(len(first.Turns[0].Content)) ||
		first.Turns[0].Content+rest.Turns[0].Content != long {
		t.Fatal("resumed turn did not reconstruct the original")
	}
	if _, err := s.QueryConversation(ctx, q, false, "different-caller"); err == nil {
		t.Fatal("accepted another caller's cursor")
	}

	q = envelope.ConversationQuery{SessionID: "source", Role: "user", Order: "oldest", Limit: 1, BeforeTurn: matches.Turns[0].ID + 10}
	first, err = s.QueryConversation(ctx, q, false, "source")
	if err != nil || len(first.Turns) != 1 || first.Turns[0].Content != "first" || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %+v, %v", first, err)
	}
	if err := s.AddConversationTurn("source", "user", "new"); err != nil {
		t.Fatal(err)
	}
	q.Cursor = first.NextCursor
	rest, err = s.QueryConversation(ctx, q, false, "source")
	if err != nil || len(rest.Turns) != 1 || rest.Turns[0].Content != "last" || rest.NextCursor != "" {
		t.Fatalf("snapshot paging included a new turn: %+v, %v", rest, err)
	}
	q.Role = "assistant"
	if _, err := s.QueryConversation(ctx, q, false, "source"); err == nil {
		t.Fatal("accepted cursor with changed filters")
	}
}
