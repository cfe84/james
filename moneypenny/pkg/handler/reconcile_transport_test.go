package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

type stalledNotificationTransport struct {
	started chan struct{}
	release chan struct{}
}

func (w *stalledNotificationTransport) Write(b []byte) (int, error) {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return len(b), nil
}

func TestReconcilePersistsBeforeStalledNotificationTransport(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const sessionID = "123e4567-e89b-12d3-a456-426614174002"
	if err := st.CreateSession(&store.Session{SessionID: sessionID, Name: "test", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	transport := &stalledNotificationTransport{started: make(chan struct{}), release: make(chan struct{})}
	writer := envelope.NewNotificationWriter(transport)
	st.SetNotificationWriter(writer)

	done := make(chan error, 1)
	go func() { done <- st.AddConversationTurn(sessionID, "assistant", "persisted while disconnected") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("conversation mutation blocked on notification transport")
	}

	select {
	case <-transport.started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("notification was not attempted after the committed mutation")
	}
	close(transport.release)
	h := &Handler{store: st}
	resp := h.Handle(context.Background(), &envelope.Command{
		Method: "reconcile_session",
		Data:   json.RawMessage(`{"session_id":"` + sessionID + `","revision":0,"generation":1}`),
	})
	if resp.Status != envelope.StatusSuccess {
		t.Fatalf("reconcile status = %s, response = %+v", resp.Status, resp)
	}
	got := resp.Data.(envelope.SessionReconcile)
	if got.Revision != 1 || got.Total != 1 || len(got.Conversation) != 1 ||
		got.Conversation[0].Content != "persisted while disconnected" {
		t.Fatalf("reconcile did not observe committed turn: %+v", got)
	}
}
