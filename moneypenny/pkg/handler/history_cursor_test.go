package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func testTurn(content string) *store.ConversationTurn {
	return &store.ConversationTurn{ID: 7, Role: "assistant", Content: content, CreatedAt: time.Unix(0, 0).UTC()}
}

func TestBoundedConversationUsesEncodedJSONBytes(t *testing.T) {
	turn := testTurn(strings.Repeat(`é "quoted"`, 100))
	full := envelope.SessionConversation{
		SessionID: "s", Conversation: []envelope.ConversationTurn{{ID: turn.ID, Role: turn.Role, Content: turn.Content, CreatedAt: turn.CreatedAt.Format("2006-01-02T15:04:05Z")}},
		Total: 1, Revision: 1, Generation: 1,
	}
	wire, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	got, next, more, _, err := boundedConversation("s", []*store.ConversationTurn{turn}, 1, 1, 1, 0, 0, len(wire), false)
	if err != nil || len(got) != 1 || next != "" || more {
		t.Fatalf("bounded page = %d next=%q more=%v", len(got), next, more)
	}
	tooSmall, nextSmall, _, _, err := boundedConversation("s", []*store.ConversationTurn{turn}, 1, 1, 1, 0, 0, len(wire)-1, false)
	if err != nil || len(tooSmall) != 1 || tooSmall[0].Content == turn.Content || nextSmall == "" {
		t.Fatalf("expected deterministic UTF-8 chunk: page=%+v next=%q", tooSmall, nextSmall)
	}
}

func TestBoundedConversationRejectsInvalidChunkPosition(t *testing.T) {
	turn := testTurn("é")
	if _, _, _, _, err := boundedConversation("s", []*store.ConversationTurn{turn}, 1, 1, 1, 0, 1, 1024, false); err == nil {
		t.Fatal("cursor splitting a UTF-8 sequence was accepted")
	}
	if _, _, _, _, err := boundedConversation("s", []*store.ConversationTurn{turn}, 1, 1, 1, 0, 3, 1024, false); err == nil {
		t.Fatal("cursor beyond content was accepted")
	}
}

func TestHistoryCursorRejectsMalformedAndStale(t *testing.T) {
	if _, err := decodeHistoryCursor("not-base64", 1); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	cursor := encodeHistoryCursor(historyCursor{Version: 1, Generation: 2, Offset: 3, TurnID: 9})
	if _, err := decodeHistoryCursor(cursor, 1); err == nil {
		t.Fatal("stale cursor accepted")
	}
	decoded, err := decodeHistoryCursor(cursor, 2)
	if err != nil || decoded.Offset != 3 || decoded.TurnID != 9 {
		t.Fatalf("decoded cursor = %+v, err=%v", decoded, err)
	}
}
