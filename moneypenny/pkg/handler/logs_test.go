package handler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestTailLogFileReturnsRequestedFinalLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moneypenny.log")
	content := "first\nsecond\nthird\nfourth\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	got, err := tailLogFile(path, 2)
	if err != nil {
		t.Fatalf("tailLogFile: %v", err)
	}
	if got.Content != "third\nfourth" {
		t.Errorf("content = %q, want %q", got.Content, "third\nfourth")
	}
	if got.Lines != 2 {
		t.Errorf("lines = %d, want 2", got.Lines)
	}
	if got.Truncated {
		t.Error("truncated = true, want false")
	}
}

func TestTailLogFileBoundsLargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moneypenny.log")
	content := strings.Repeat("older log line that should not be returned\n", 60_000) + "final line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	got, err := tailLogFile(path, 1)
	if err != nil {
		t.Fatalf("tailLogFile: %v", err)
	}
	if got.Content != "final line" {
		t.Errorf("content = %q, want final line", got.Content)
	}
	if got.Lines != 1 {
		t.Errorf("lines = %d, want 1", got.Lines)
	}
	if !got.Truncated {
		t.Error("truncated = false, want true")
	}
}

func TestHandleGetLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moneypenny.log")
	if err := os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	h := &Handler{logFile: path}
	resp := h.Handle(context.Background(), &envelope.Command{
		Method:    "get_logs",
		RequestID: "request-1",
		Data:      json.RawMessage(`{"lines":2}`),
	})
	if resp.Status != envelope.StatusSuccess {
		t.Fatalf("status = %q, want %q", resp.Status, envelope.StatusSuccess)
	}

	data, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var got envelope.GetLogsResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.Content != "second\nthird" || got.Lines != 2 || got.Truncated {
		t.Errorf("response = %+v, want final two untruncated lines", got)
	}
}
