package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func TestStopSessionDiscardsLateAgentResult(t *testing.T) {
	s, err := store.New(t.TempDir() + "/stop.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	const sessionID = "11111111-1111-4111-8111-111111111111"
	if err := s.CreateSession(&store.Session{SessionID: sessionID, Name: "stop", Agent: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionStatus(sessionID, store.StateWorking); err != nil {
		t.Fatal(err)
	}
	h := New(s, agent.New(nil), "test", t.TempDir())
	t.Cleanup(h.Close)

	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		close(started)
		<-release
		close(finished)
		return &agent.Result{Text: "late answer"}, nil
	}
	h.startAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "agent", Prompt: "prompt"})
	<-started

	data, err := json.Marshal(envelope.SessionIDData{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	response := h.stopSession(context.Background(), &envelope.Command{RequestID: "stop", Data: data})
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("stop response = %#v", response)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("late runner did not return")
	}
	deadline := time.Now().Add(time.Second)
	for {
		h.runsMu.Lock()
		active := h.activeRuns[sessionID]
		h.runsMu.Unlock()
		if active == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled run remained active")
		}
		time.Sleep(time.Millisecond)
	}

	turns, err := s.GetConversation(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if turn.Role == "assistant" && turn.Content == "late answer" {
			t.Fatalf("stopped run persisted late response: %#v", turns)
		}
	}
	sess, err := s.GetSession(sessionID)
	if err != nil || sess.Status != store.StateIdle {
		t.Fatalf("session after stop = %#v, %v", sess, err)
	}
}
