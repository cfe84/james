package handler

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func TestDiagnosticsSnapshotAndExplicitScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(st, agent.New(log.New(io.Discard, "", 0)), "test", dir)
	h.SetDiagnostics(path, func() envelope.DispatcherDiagnostics {
		return envelope.DispatcherDiagnostics{Reads: envelope.WorkerDiagnostics{Active: 2, Workers: 2}}
	})
	logPath := filepath.Join(dir, "test.log")
	if err := os.WriteFile(logPath, []byte("secret log content"), 0600); err != nil {
		t.Fatal(err)
	}
	h.SetLogFile(logPath)
	if err := st.CreateSession(&store.Session{SessionID: "s1", Name: "private-name", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		data         string
		status       string
		session      bool
		sessionError bool
	}{
		{`{}`, envelope.StatusSuccess, false, false},
		{`{"session_id":"s1","scan_session":true}`, envelope.StatusSuccess, true, false},
		{`{"session_id":"missing","scan_session":true}`, envelope.StatusSuccess, false, true},
		{`{"session_id":"s1"}`, envelope.StatusError, false, false},
		{`{"scan_session":true}`, envelope.StatusError, false, false},
		{`{"scan_session":"yes"}`, envelope.StatusError, false, false},
		{`{"session_id":"` + strings.Repeat("x", 257) + `","scan_session":true}`, envelope.StatusError, false, false},
		{`{"ignored":"` + strings.Repeat("x", 1024) + `"}`, envelope.StatusError, false, false},
	} {
		resp := h.Handle(context.Background(), &envelope.Command{Method: "get_diagnostics", RequestID: "diag", Data: json.RawMessage(tc.data)})
		if resp.Status != tc.status {
			t.Fatalf("data=%s status=%s want=%s", tc.data, resp.Status, tc.status)
		}
		if resp.Status != envelope.StatusSuccess {
			continue
		}
		got := resp.Data.(envelope.GetDiagnosticsResponse)
		if got.PID != os.Getpid() || got.Runtime.HeapAllocBytes == 0 || got.Runtime.Goroutines == 0 || got.Dispatcher.Reads.Active != 2 {
			t.Fatalf("invalid snapshot: %+v", got)
		}
		if got.Process.RSSBytes == nil && got.Process.Error == "" {
			t.Fatal("unavailable process memory lacks error")
		}
		if got.Files.Database.Bytes == nil || got.Files.Log.Bytes == nil || *got.Files.Log.Bytes != 18 || got.Database.MaxOpenConnections != 4 {
			t.Fatalf("file/pool snapshot: %+v", got)
		}
		if (got.Session != nil) != tc.session || (got.SessionError != "") != tc.sessionError {
			t.Fatalf("session scan result: %+v", got)
		}
		b, err := resp.Marshal()
		if err != nil || len(b) > 4096 || strings.Contains(string(b), "secret log") || strings.Contains(string(b), "private-name") {
			t.Fatalf("diagnostics not bounded/content-free: bytes=%d error=%v", len(b), err)
		}
	}
	if file := diagnosticFile(filepath.Join(dir, "missing")); file.Bytes != nil || file.Error == "" {
		t.Fatalf("missing file looks successful: %+v", file)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	light := h.Handle(context.Background(), &envelope.Command{Method: "get_diagnostics", Data: json.RawMessage(`{}`)})
	if light.Status != envelope.StatusSuccess || light.Data.(envelope.GetDiagnosticsResponse).Session != nil {
		t.Fatal("lightweight snapshot must not query even a closed database")
	}
	resp := h.Handle(context.Background(), &envelope.Command{Method: "get_diagnostics", Data: json.RawMessage(`{"session_id":"s1","scan_session":true}`)})
	got := resp.Data.(envelope.GetDiagnosticsResponse)
	if got.Session != nil || got.SessionError == "" {
		t.Fatal("closed store produced success-shaped session metrics")
	}
}
