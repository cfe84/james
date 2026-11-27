package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
)

func TestCrossHostConversationTransfer(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	source, _ := gadgetTestHandler(t)
	target, _ := gadgetTestHandler(t)
	t.Cleanup(source.Close)
	t.Cleanup(target.Close)
	target.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		return &agent.Result{}, nil
	}
	if err := target.store.AddConversationTurn(gadgetOtherSession, "user", "existing target turn"); err != nil {
		t.Fatal(err)
	}
	call := func(h *Handler, method string, data any) *envelope.Response {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return h.Handle(context.Background(), &envelope.Command{Method: method, Data: raw})
	}

	const count = 101
	for i := range count {
		if err := source.store.AddConversationTurnFrom(gadgetSession, "user", fmt.Sprint(i), gadgetOtherSession, "Sender"); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.store.AddConversationTurn(gadgetSession, "artifact", "source-owned artifact"); err != nil {
		t.Fatal(err)
	}
	original, err := source.store.GetConversation(gadgetSession)
	if err != nil {
		t.Fatal(err)
	}
	id := envelope.SessionIDData{SessionID: gadgetSession}
	export := call(source, "export_conversation", id)
	history, ok := export.Data.(json.RawMessage)
	if export.Status != envelope.StatusSuccess || !ok {
		t.Fatalf("export failed: %+v", export)
	}

	const duplicateID = "00000000-0000-0000-0000-000000000075"
	create := map[string]any{
		"session_id": duplicateID, "name": "Duplicate", "agent": "git", "prompt": "handoff", "path": t.TempDir(),
		"imported_history": json.RawMessage(`[{"role":"user","created_at":"invalid"}]`),
	}
	if resp := call(target, "create_session_with_history", create); resp.ErrorCode != envelope.ErrInvalidRequest {
		t.Fatalf("malformed history was not rejected: %+v", resp)
	}
	if sess, err := target.store.GetSession(duplicateID); err != nil || sess != nil {
		t.Fatalf("malformed transfer created a session: %+v, %v", sess, err)
	}
	create["imported_history"] = history
	if resp := call(target, "create_session_with_history", create); resp.Status != envelope.StatusSuccess {
		t.Fatalf("import failed: %+v", resp)
	}
	copied, err := target.store.GetConversation(duplicateID)
	if err != nil || len(copied) != count+1 || copied[count].Content != "handoff" {
		t.Fatalf("expected complete history followed by handoff: got %d turns, error %v", len(copied), err)
	}
	for i, got := range copied[:count] {
		want := original[i]
		if got.Content != want.Content || got.Role != want.Role || got.SourceSessionID != want.SourceSessionID ||
			got.SourceName != want.SourceName || !got.CreatedAt.Equal(want.CreatedAt) || got.ID == want.ID {
			t.Fatalf("turn %d: got %+v, want metadata from %+v with fresh ID", i, got, want)
		}
	}
	if err := source.store.AddConversationTurn(gadgetSession, "assistant", strings.Repeat("<", envelope.MaxTransferredHistoryBytes/6)); err != nil {
		t.Fatal(err)
	}
	if resp := call(source, "export_conversation", id); resp.ErrorCode != envelope.ErrInvalidRequest {
		t.Fatalf("encoded transcript exceeds limit but was not rejected: %s (%s)", resp.Status, resp.ErrorCode)
	}
}
