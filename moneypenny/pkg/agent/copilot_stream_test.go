package agent

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeCopilotCmd returns an *exec.Cmd that emits the given JSONL fixture on
// stdout (via `cat`), mimicking a copilot --stream process.
func fakeCopilotCmd(t *testing.T, jsonl string) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(fixture, []byte(jsonl), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return exec.Command("cat", fixture)
}

func newTestRunner(persist PersistentActivityFunc) *Runner {
	r := New(log.New(io.Discard, "", 0))
	r.onPersistentActivity = persist
	return r
}

// TestCopilotStreamingAccumulatesMessages verifies that when copilot emits a
// substantive answer, then a tool call, then a short closing message, the final
// reply is the concatenation of ALL assistant.message text blocks (not just the
// last one) and that those blocks are NOT persisted as agent_text turns.
func TestCopilotStreamingAccumulatesMessages(t *testing.T) {
	var persisted [][2]string // {role/eventType, content}
	r := newTestRunner(func(_, eventType, content string) {
		persisted = append(persisted, [2]string{eventType, content})
	})

	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"assistant.message","data":{"content":"First substantive answer.","toolRequests":[{"name":"bash","arguments":{"command":"echo hi","description":"say hi"}}]}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"echo hi"}}}
{"type":"tool.execution_complete","data":{}}
{"type":"assistant.reasoning","data":{"content":"some private reasoning"}}
{"type":"assistant.message","data":{"content":"Done."}}
`

	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sess1", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}

	want := "First substantive answer.\n\nDone."
	if res.Text != want {
		t.Errorf("reply mismatch:\n got: %q\nwant: %q", res.Text, want)
	}

	// assistant.message text must NOT be persisted as agent_text. Only the
	// reasoning event should persist (as "thinking").
	for _, p := range persisted {
		if p[0] == "text" {
			t.Errorf("assistant.message text should not be persisted, got %v", p)
		}
	}
	var gotThinking bool
	for _, p := range persisted {
		if p[0] == "thinking" && p[1] == "some private reasoning" {
			gotThinking = true
		}
	}
	if !gotThinking {
		t.Errorf("expected reasoning persisted as thinking, got %v", persisted)
	}
}

// TestCopilotStreamingOnlyToolCalls verifies that a turn producing no
// assistant.message text yields an empty reply (handler then skips the turn).
func TestCopilotStreamingOnlyToolCalls(t *testing.T) {
	r := newTestRunner(func(string, string, string) {})
	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"echo hi"}}}
{"type":"tool.execution_complete","data":{}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sess2", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	if res.Text != "" {
		t.Errorf("expected empty reply, got %q", res.Text)
	}
}

// TestCopilotStreamingTrimsAndJoins verifies each segment is trimmed before the
// blank-line join so provider newlines don't compound.
func TestCopilotStreamingTrimsAndJoins(t *testing.T) {
	r := newTestRunner(func(string, string, string) {})
	jsonl := `{"type":"assistant.message","data":{"content":"\n\nPart one.\n\n"}}
{"type":"assistant.message","data":{"content":"  Part two.  "}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sess3", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	want := "Part one.\n\nPart two."
	if res.Text != want {
		t.Errorf("reply mismatch:\n got: %q\nwant: %q", res.Text, want)
	}
}
