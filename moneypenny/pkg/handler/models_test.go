package handler

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func TestListSessionsIncludesSelectedModel(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, session := range []*store.Session{
		{SessionID: "selected", Agent: "opencode", Model: "azure/gpt-6-luna"},
		{SessionID: "default", Agent: "copilot"},
	} {
		if err := st.CreateSession(session); err != nil {
			t.Fatal(err)
		}
	}
	h := New(st, agent.New(nil), "test", t.TempDir())
	resp := h.listSessions(context.Background(), &envelope.Command{RequestID: "list"})
	if resp.Status != envelope.StatusSuccess {
		t.Fatalf("list sessions failed: %v", resp)
	}
	infos := resp.Data.([]envelope.SessionInfo)
	models := make(map[string]string)
	for _, info := range infos {
		models[info.SessionID] = info.Model
	}
	if len(models) != 2 || models["selected"] != "azure/gpt-6-luna" || models["default"] != "" {
		t.Fatalf("unexpected session models: %v", models)
	}
}

func TestReadCopilotModelCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantError string
		want                  []envelope.ModelInfo
	}{
		{
			name: "selectable models",
			body: `{"jsonrpc":"2.0","id":1,"result":{"models":[{"id":"auto","name":"Copilot Fusion"},{"id":"gpt-6.2-sol","name":"GPT-6.2 Sol"},{"id":"gpt-6.2-sol"},{"id":"custom/model"}],"imageModels":[{"id":"image-only"}]}}`,
			want: []envelope.ModelInfo{
				{Name: "Copilot Fusion", Value: "auto"},
				{Name: "GPT-6.2 Sol", Value: "gpt-6.2-sol"},
				{Name: "custom/model", Value: "custom/model"},
			},
		},
		{
			name:      "RPC error",
			body:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`,
			wantError: "Method not found",
		},
		{
			name:      "empty catalog",
			body:      `{"jsonrpc":"2.0","id":1,"result":{"models":[]}}`,
			wantError: "returned no models",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(tc.body), tc.body)
			got, err := readCopilotModelCatalog(strings.NewReader(frame))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || got != nil {
					t.Fatalf("got %v, %v; want error containing %q", got, err, tc.wantError)
				}
			} else if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
