package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/store"
)

func TestRootMemoryFailureSurfacesAndReturnsIdle(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	if err := st.CreateSession(&store.Session{SessionID: sid, Name: "test", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionStatus(sid, store.StateWorking); err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: st, dataDir: t.TempDir(), vlog: func(string, ...interface{}) {}}
	root := filepath.Join(h.dataDir, "sessions", sid, "memory", "README.md")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	// No runner is configured: preparing unreadable memory must abort before execution.
	h.runAgent(sid, agent.RunParams{SessionID: sid, Agent: "claude", Prompt: "Task"})
	sess, err := st.GetSession(sid)
	if err != nil || sess == nil || sess.Status != store.StateIdle {
		t.Fatalf("session not recovered to idle: %+v, %v", sess, err)
	}
	turns, err := st.GetConversation(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Role != "system" || !strings.Contains(turns[0].Content, "memory") {
		t.Fatalf("missing visible root-read failure: %+v", turns)
	}
}
