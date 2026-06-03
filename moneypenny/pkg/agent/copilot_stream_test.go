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

// TestCopilotStreamingPhaseClassifiesReply verifies that when Copilot tags
// messages with a phase, the reply is exactly the phase=="final_answer"
// message(s), and phase=="commentary" preambles go to the train of thought —
// regardless of tool-call position. This is the preferred classification path.
func TestCopilotStreamingPhaseClassifiesReply(t *testing.T) {
	var persisted [][2]string
	r := newTestRunner(func(_, eventType, content string) {
		persisted = append(persisted, [2]string{eventType, content})
	})
	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"assistant.message","data":{"phase":"commentary","content":"Got it - creating the file now.","toolRequests":[{"name":"bash","arguments":{"command":"echo hi","description":"say hi"}}]}}
{"type":"assistant.message","data":{"phase":"commentary","content":"","toolRequests":[{"name":"bash","arguments":{"command":"echo hi"}}]}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"echo hi"}}}
{"type":"tool.execution_complete","data":{}}
{"type":"assistant.message","data":{"phase":"final_answer","content":"Done - the file is created."}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sessP", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	if res.Text != "Done - the file is created." {
		t.Errorf("expected final_answer reply, got %q", res.Text)
	}
	var gotPreamble bool
	for _, p := range persisted {
		if p[0] == "text" && p[1] == "Got it - creating the file now." {
			gotPreamble = true
		}
		if p[1] == "Done - the file is created." {
			t.Errorf("final_answer should not be persisted as train of thought, got %v", p)
		}
	}
	if !gotPreamble {
		t.Errorf("expected commentary preamble persisted as agent_text, got %v", persisted)
	}
}

// TestCopilotStreamingPhaseNoFinalAnswer verifies that when Copilot supplies
// phase labels but never emits a final_answer (e.g. the turn ended on tool
// activity), the reply is empty and the commentary is kept as train of thought.
// The positional fallback must NOT kick in once phases are present.
func TestCopilotStreamingPhaseNoFinalAnswer(t *testing.T) {
	var persisted [][2]string
	r := newTestRunner(func(_, eventType, content string) {
		persisted = append(persisted, [2]string{eventType, content})
	})
	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"assistant.message","data":{"phase":"commentary","content":"Let me check the logs."}}
{"type":"assistant.message","data":{"phase":"commentary","content":"","toolRequests":[{"name":"bash","arguments":{"command":"cat log","description":"read"}}]}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"cat log"}}}
{"type":"tool.execution_complete","data":{}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sessPN", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	if res.Text != "" {
		t.Errorf("expected empty reply when no final_answer, got %q", res.Text)
	}
	var gotCommentary bool
	for _, p := range persisted {
		if p[0] == "text" && p[1] == "Let me check the logs." {
			gotCommentary = true
		}
	}
	if !gotCommentary {
		t.Errorf("expected commentary persisted as agent_text, got %v", persisted)
	}
}

// TestCopilotStreamingPreamblesGoToThoughts verifies that pre-tool narration
// (assistant.message events carrying tool calls) is routed to the train of
// thought (persisted as agent_text), and only the trailing no-tool message
// becomes the final reply — so the reply is just the last "turn", not every
// preamble the model emitted while working.
func TestCopilotStreamingPreamblesGoToThoughts(t *testing.T) {
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

	// Only the trailing no-tool message is the reply.
	want := "Done."
	if res.Text != want {
		t.Errorf("reply mismatch:\n got: %q\nwant: %q", res.Text, want)
	}

	// The tool-bearing preamble must be persisted as agent_text ("text"), and
	// the reasoning as "thinking" — both in the train of thought.
	var gotPreamble, gotThinking bool
	for _, p := range persisted {
		if p[0] == "text" && p[1] == "First substantive answer." {
			gotPreamble = true
		}
		if p[0] == "thinking" && p[1] == "some private reasoning" {
			gotThinking = true
		}
		// The reply must NOT be persisted here (handler stores it as the turn).
		if p[1] == "Done." {
			t.Errorf("reply text should not be persisted as train of thought, got %v", p)
		}
	}
	if !gotPreamble {
		t.Errorf("expected preamble persisted as agent_text, got %v", persisted)
	}
	if !gotThinking {
		t.Errorf("expected reasoning persisted as thinking, got %v", persisted)
	}
}

// TestCopilotStreamingBundledAnswerFallback verifies that when the model bundles
// its answer with a housekeeping tool call and emits no trailing no-tool message,
// the reply falls back to that last message (so a real answer is never hidden
// entirely in the train of thought).
func TestCopilotStreamingBundledAnswerFallback(t *testing.T) {
	var persisted [][2]string
	r := newTestRunner(func(_, eventType, content string) {
		persisted = append(persisted, [2]string{eventType, content})
	})
	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"assistant.message","data":{"content":"The answer is 42.","toolRequests":[{"name":"bash","arguments":{"command":"git commit","description":"commit"}}]}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"git commit"}}}
{"type":"tool.execution_complete","data":{}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sessF", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	if res.Text != "The answer is 42." {
		t.Errorf("expected fallback reply, got %q", res.Text)
	}
	for _, p := range persisted {
		if p[1] == "The answer is 42." {
			t.Errorf("fallback reply should not be persisted as train of thought, got %v", p)
		}
	}
}

// TestCopilotStreamingEmptyToolMessageIsBoundary verifies gpt-5.5's edge case:
// narration with NO tool call, followed by a SEPARATE empty-content message that
// carries the tool call, must not be promoted to the reply. The empty tool-only
// message acts as a boundary so the narration stays in the train of thought and
// the reply is empty.
func TestCopilotStreamingEmptyToolMessageIsBoundary(t *testing.T) {
	var persisted [][2]string
	r := newTestRunner(func(_, eventType, content string) {
		persisted = append(persisted, [2]string{eventType, content})
	})
	jsonl := `{"type":"assistant.turn_start","data":{}}
{"type":"assistant.message","data":{"content":"I'll inspect X."}}
{"type":"assistant.message","data":{"content":"","toolRequests":[{"name":"bash","arguments":{"command":"ls","description":"list"}}]}}
{"type":"tool.execution_start","data":{"toolName":"bash","arguments":{"command":"ls"}}}
{"type":"tool.execution_complete","data":{}}
`
	buf := newActivityBuffer(30)
	res, err := r.runCopilotStreaming(fakeCopilotCmd(t, jsonl), buf, "sessB", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("runCopilotStreaming: %v", err)
	}
	if res.Text != "" {
		t.Errorf("expected empty reply (narration is not the answer), got %q", res.Text)
	}
	var gotNarration bool
	for _, p := range persisted {
		if p[0] == "text" && p[1] == "I'll inspect X." {
			gotNarration = true
		}
		if p[1] == "" {
			t.Errorf("empty tool-only message should not be persisted, got %v", p)
		}
	}
	if !gotNarration {
		t.Errorf("expected narration persisted as agent_text, got %v", persisted)
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
