package handler

import (
	"context"
	"encoding/json"
	"testing"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func TestReconcileSessionUnchangedReturnsWatermarkOnly(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	if err := st.CreateSession(&store.Session{SessionID: sessionID, Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddConversationTurn(sessionID, "user", "hello"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: st}
	resp := h.Handle(context.Background(), &envelope.Command{
		Method: "reconcile_session",
		Data:   json.RawMessage(`{"session_id":"` + sessionID + `","revision":1,"generation":1}`),
	})
	if resp.Status != envelope.StatusSuccess {
		t.Fatalf("reconcile status = %s, response = %+v", resp.Status, resp)
	}
	got, ok := resp.Data.(envelope.SessionReconcile)
	if !ok {
		t.Fatalf("reconcile data type = %T", resp.Data)
	}
	if got.Revision != 1 || got.Generation != 1 || got.Total != 1 {
		t.Fatalf("watermark = %+v", got)
	}
	if got.Conversation != nil {
		t.Fatalf("unchanged response included conversation: %+v", got.Conversation)
	}
}

func TestReconcileSessionChangedReturnsBoundedConversation(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const sessionID = "123e4567-e89b-12d3-a456-426614174001"
	if err := st.CreateSession(&store.Session{SessionID: sessionID, Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddConversationTurn(sessionID, "assistant", "reply"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: st}
	resp := h.Handle(context.Background(), &envelope.Command{
		Method: "reconcile_session",
		Data:   json.RawMessage(`{"session_id":"` + sessionID + `","revision":0,"generation":1}`),
	})
	if resp.Status != envelope.StatusSuccess {
		t.Fatalf("reconcile status = %s, response = %+v", resp.Status, resp)
	}
	got, ok := resp.Data.(envelope.SessionReconcile)
	if !ok {
		t.Fatalf("reconcile data type = %T", resp.Data)
	}
	if len(got.Conversation) != 1 || got.Conversation[0].Content != "reply" {
		t.Fatalf("conversation = %+v", got.Conversation)
	}
	if got.Revision != 1 || got.Total != 1 || got.Truncated {
		t.Fatalf("changed response metadata = %+v", got)
	}
}
