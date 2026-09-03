package handler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestSanitizeAttachmentNameCrossPlatformPaths(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unix path", input: "/tmp/report.txt", want: "report.txt"},
		{name: "windows path", input: `C:\Users\alice\report.txt`, want: "report.txt"},
		{name: "mixed path", input: `..\..\report.txt`, want: "report.txt"},
		{name: "dot", input: ".", want: ""},
		{name: "dot dot", input: "..", want: ""},
		{name: "empty", input: "  ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAttachmentName(tt.input); got != tt.want {
				t.Fatalf("sanitizeAttachmentName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSessionDirPathContainsSession(t *testing.T) {
	h := &Handler{dataDir: t.TempDir()}
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"

	path, err := h.sessionDirPath(sessionID)
	if err != nil {
		t.Fatalf("sessionDirPath() returned error: %v", err)
	}
	want := filepath.Join(h.dataDir, "sessions", sessionID)
	if path != want {
		t.Fatalf("sessionDirPath() = %q, want %q", path, want)
	}

	for _, id := range []string{"../../..", "/tmp/session", "not-a-uuid", "123E4567-e89b-12d3-a456-426614174000"} {
		if _, err := h.sessionDirPath(id); err == nil {
			t.Errorf("sessionDirPath(%q) succeeded, want error", id)
		}
	}
}

func TestCreateImportAndDeleteRejectTraversalSessionID(t *testing.T) {
	h := &Handler{dataDir: t.TempDir()}
	ctx := context.Background()

	createData, _ := json.Marshal(envelope.CreateSessionData{
		SessionID: "../../..",
		Agent:     "claude",
		Name:      "test",
		Prompt:    "test",
	})
	importData, _ := json.Marshal(envelope.ImportSessionData{
		SessionID: "../../..",
		Agent:     "claude",
		Name:      "test",
		Path:      t.TempDir(),
	})
	deleteData, _ := json.Marshal(envelope.SessionIDData{SessionID: "../../.."})

	for name, handle := range map[string]func() *envelope.Response{
		"create": func() *envelope.Response {
			return h.createSession(ctx, &envelope.Command{RequestID: "create", Data: createData})
		},
		"import": func() *envelope.Response {
			return h.importSession(ctx, &envelope.Command{RequestID: "import", Data: importData})
		},
		"delete": func() *envelope.Response {
			return h.deleteSession(ctx, &envelope.Command{RequestID: "delete", Data: deleteData})
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp := handle()
			if resp.Status != envelope.StatusError || resp.ErrorCode != envelope.ErrInvalidRequest {
				t.Fatalf("response = %#v, want invalid request", resp)
			}
		})
	}
}
