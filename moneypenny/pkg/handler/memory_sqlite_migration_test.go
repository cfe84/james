package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"james/moneypenny/pkg/memory"
	"james/moneypenny/pkg/store"
)

func TestSQLiteMemoryMigrationAllSessionsAndRetry(t *testing.T) {
	const (
		fileSession   = "00000000-0000-0000-0000-000000000001"
		treeSession   = "00000000-0000-0000-0000-000000000002"
		blobSession   = "00000000-0000-0000-0000-000000000003"
		emptySession  = "00000000-0000-0000-0000-000000000004"
		brokenSession = "00000000-0000-0000-0000-000000000005"
	)
	dir := t.TempDir()
	s, err := store.New(filepath.Join(dir, "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := &Handler{store: s, dataDir: dir}
	for _, id := range []string{fileSession, treeSession, blobSession, emptySession, brokenSession} {
		if err := s.CreateSession(&store.Session{SessionID: id, Agent: "copilot"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpdateSessionStatus(fileSession, "completed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemoryNode(treeSession, "topic", "Title", "Summary", "Body"); err != nil {
		t.Fatal(err)
	}
	blob := "\n" + strings.Repeat("old memory ", 500) + "\n"
	if err := s.SetMemory(blobSession, blob); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(dir, "sessions", fileSession, "memory")
	if err := os.MkdirAll(fileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, "README.md"), []byte("file root"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "sessions", brokenSession, "memory", "README.md")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.MigrateMemoryToSQLite(); err == nil || !strings.Contains(err.Error(), "safe to retry") {
		t.Fatalf("migration failure not surfaced: %v", err)
	}
	for _, tc := range []struct{ session, path, body string }{
		{fileSession, "", "file root"},
		{treeSession, "topic", "Body"},
		{blobSession, "notes", blob},
	} {
		root := filepath.Join(dir, "sessions", tc.session, "memory")
		n, err := memory.Get(root, tc.path)
		if err != nil || n == nil || n.Body != tc.body {
			t.Fatalf("%s migration: %+v, %v", tc.session, n, err)
		}
	}
	emptyRoot := filepath.Join(dir, "sessions", emptySession, "memory")
	if _, err := os.Stat(memory.DatabasePath(emptyRoot)); err != nil {
		t.Fatalf("empty session not initialized: %v", err)
	}
	if got, err := s.GetMemory(blobSession); err != nil || got != blob {
		t.Fatal("main-db blob changed")
	}
	if count, err := s.MemoryNodeCount(blobSession); err != nil || count != 0 {
		t.Fatal("migration wrote to main-db nodes")
	}
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	if err := h.MigrateMemoryToSQLite(); err != nil {
		t.Fatalf("migration retry: %v", err)
	}
	if err := os.Remove(filepath.Join(fileDir, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := h.MigrateSessionMemoryToSQLite(fileSession); err != nil {
		t.Fatal(err)
	}
	if n, err := memory.Get(fileDir, ""); err != nil || n == nil || n.Body != "file root" {
		t.Fatal("completed migration depended on backup file")
	}
}
