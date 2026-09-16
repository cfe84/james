package handler

import (
	"context"
	"encoding/json"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func newOperationRepairHandler(t *testing.T) (*Handler, *store.Store, string) {
	t.Helper()
	s, err := store.New(t.TempDir() + "/operations.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sessionID := "11111111-1111-4111-8111-111111111111"
	if err := s.CreateSession(&store.Session{SessionID: sessionID, Name: "repair", Agent: "agent", Status: store.StateWorking}); err != nil {
		t.Fatal(err)
	}
	h := New(s, agent.New(nil), "test", t.TempDir())
	t.Cleanup(h.Close)
	return h, s, sessionID
}

func operationCommand(t *testing.T, method, sessionID, prompt, operationID string) *envelope.Command {
	t.Helper()
	data, err := json.Marshal(envelope.ContinueSessionData{
		SessionID: sessionID, Prompt: prompt, OperationID: operationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &envelope.Command{Method: method, RequestID: method + "-request", Data: data}
}

func responseMap(t *testing.T, response *envelope.Response) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestContinueReplayRepairReturnsReloadedQueuedMetadata(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	cmd := operationCommand(t, "continue_session", sessionID, "queued prompt", "repair-continue")
	if response := h.reserveOperation(sessionID, "repair-continue", envelope.ContinueSessionData{
		SessionID: sessionID, Prompt: "queued prompt", OperationID: "repair-continue",
	}, false, "reserve"); response != nil {
		t.Fatalf("reserve returned response: %#v", response)
	}
	if err := s.QueuePromptChannelFromOperation(sessionID, "queued prompt", "", "", "", "", "", "", 0, false, "repair-continue"); err != nil {
		t.Fatal(err)
	}
	response := h.Handle(context.Background(), cmd)
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("status = %s, want success", response.Status)
	}
	result := responseMap(t, response)
	if result["status"] != store.OperationQueued || result["queued"] != true {
		t.Fatalf("replay metadata = %#v, want queued", result)
	}
	op, found, err := s.GetClientOperation(sessionID, "repair-continue")
	if err != nil || !found {
		t.Fatalf("operation lookup: found=%v err=%v", found, err)
	}
	if op.Status != store.OperationQueued || len(op.Response) == 0 {
		t.Fatalf("persisted operation = %#v, want queued response", op)
	}
}

func TestQueuePromptRepairsFailedOperationWithoutDuplicateRow(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	data := envelope.ContinueSessionData{SessionID: sessionID, Prompt: "retry me", OperationID: "repair-queue"}
	if response := h.reserveOperation(sessionID, data.OperationID, data, true, "reserve"); response != nil {
		t.Fatalf("reserve returned response: %#v", response)
	}
	if updated, err := s.UpdateClientOperationStatus(sessionID, data.OperationID, store.OperationAccepted, store.OperationFailed, map[string]interface{}{
		"status": store.OperationFailed, "operation_id": data.OperationID,
	}); err != nil || !updated {
		t.Fatalf("mark failed: updated=%v err=%v", updated, err)
	}
	response := h.Handle(context.Background(), operationCommand(t, "queue_prompt", sessionID, data.Prompt, data.OperationID))
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("status = %s, want success", response.Status)
	}
	result := responseMap(t, response)
	if result["status"] != store.OperationQueued || result["queued"] != true {
		t.Fatalf("queue metadata = %#v, want queued", result)
	}
	response = h.Handle(context.Background(), operationCommand(t, "queue_prompt", sessionID, data.Prompt, data.OperationID))
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("retry status = %s, want success", response.Status)
	}
	group, err := s.DrainQueueGroup(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 1 || group[0].OperationID != data.OperationID {
		t.Fatalf("drained group = %#v, want one operation row", group)
	}
	group, err = s.DrainQueueGroup(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(group) != 0 {
		t.Fatalf("duplicate queue rows remained: %#v", group)
	}
}
