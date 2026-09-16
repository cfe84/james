package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func TestCompactionPromptsRespectMemoryPermissions(t *testing.T) {
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	for _, name := range []string{"claude", "copilot", "opencode"} {
		for _, yolo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/yolo=%t", name, yolo), func(t *testing.T) {
				h, _ := newMemoryAuxiliaryTestHandler(t, name)
				dir := filepath.Join(h.dataDir, "sessions", sid, "memory")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Recorded knowledge"), 0600); err != nil {
					t.Fatal(err)
				}

				for _, system := range []string{"Base instructions", compactionSeedSystemPrompt("Base instructions", "Handoff summary")} {
					params := agent.RunParams{Agent: name, Yolo: yolo, SystemPrompt: system}
					if err := h.prepareRunInstructions(sid, &params); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(params.SystemPrompt, "<session-memory>") {
						t.Fatalf("incorrect memory availability: %+v", params)
					}
					if !strings.Contains(params.SystemPrompt, "gadgets notify") {
						t.Fatal("missing memory-independent notification instructions")
					}
					for _, enabled := range []bool{false, true} {
						task := compactionTaskPrompt(enabled)
						if strings.Contains(task, "system memory contract") != enabled {
							t.Fatalf("unexpected memory task: %s", task)
						}
						if !enabled && (!strings.Contains(task, "summary alone") || !strings.Contains(task, "memory is disabled")) {
							t.Fatalf("disabled compaction is not standalone: %s", task)
						}
						if strings.Contains(task, "README.md") || strings.Contains(task, "4000") {
							t.Fatal("task duplicates system memory usage rules")
						}
					}
					if strings.Contains(params.SystemPrompt, "full history") || strings.Contains(params.SystemPrompt, "<memory-note>") {
						t.Fatal("seed overstates available memory")
					}
					if !strings.Contains(params.SystemPrompt, system) {
						t.Fatal("lost seed summary or configured instructions")
					}
				}
			})
		}
	}
}

func newMemoryAuxiliaryTestHandler(t *testing.T, name string, capabilities ...envelope.GadgetCapabilities) (*Handler, string) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	encoded := ""
	if len(capabilities) > 0 {
		raw, err := json.Marshal(capabilities[0])
		if err != nil {
			t.Fatal(err)
		}
		encoded = string(raw)
	}
	if err := st.CreateSession(&store.Session{SessionID: sid, Name: "test", Agent: name, AgentSessionID: "original-agent", GadgetCapabilities: encoded}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionStatus(sid, store.StateIdle); err != nil {
		t.Fatal(err)
	}
	return &Handler{store: st, dataDir: t.TempDir(), vlog: func(string, ...interface{}) {}}, sid
}

func TestDistillRejectsDisabledMemoryBeforeWorking(t *testing.T) {
	for _, name := range []string{"claude", "copilot", "opencode"} {
		t.Run(name, func(t *testing.T) {
			h, sid := newMemoryAuxiliaryTestHandler(t, name, envelope.GadgetCapabilities{})
			data, err := json.Marshal(envelope.DistillSessionData{SessionID: sid})
			if err != nil {
				t.Fatal(err)
			}
			response := h.distillSessionCmd(context.Background(), &envelope.Command{RequestID: "distill", Data: data})
			if response.Status != envelope.StatusError || response.ErrorCode != envelope.ErrInvalidRequest || !strings.Contains(fmt.Sprint(response.Data), "memory capability is disabled") {
				t.Fatalf("expected clear permission error: %+v", response)
			}

			sess, err := h.store.GetSession(sid)
			if err != nil || sess.Status != store.StateIdle {
				t.Fatalf("distillation changed session state: %+v, %v", sess, err)
			}
			if _, err := os.Stat(filepath.Join(h.dataDir, "sessions")); !os.IsNotExist(err) {
				t.Fatalf("rejected distillation accessed session files: %v", err)
			}
		})
	}
}

func TestDisabledMemoryRunDoesNotMigrateOrInject(t *testing.T) {
	for _, name := range []string{"claude", "copilot", "opencode"} {
		for _, yolo := range []bool{false, true} {
			h, sid := newMemoryAuxiliaryTestHandler(t, name, envelope.GadgetCapabilities{})
			root := filepath.Join(h.dataDir, "sessions", sid, "memory")
			// Malformed storage must not matter when disabled memory is untouched.
			if err := os.MkdirAll(filepath.Join(root, "README.md"), 0700); err != nil {
				t.Fatal(err)
			}
			params := agent.RunParams{Agent: name, Yolo: yolo, SystemPrompt: "Base"}
			if err := h.prepareRunInstructions(sid, &params); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(params.SystemPrompt, "<session-memory>") || strings.Contains(params.SystemPrompt, "gadgets memory set") {
				t.Fatalf("disabled memory instructions injected: %s", params.SystemPrompt)
			}
			if !strings.Contains(params.SystemPrompt, "Persistent session memory is disabled") {
				t.Fatal("missing explicit revocation guidance")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "memory.db")); !os.IsNotExist(err) {
				t.Fatalf("disabled memory was migrated: %v", err)
			}
		}
	}
}

func TestAuxiliaryMemoryPreparationFailureRecoversIdle(t *testing.T) {
	for _, operation := range []string{"compaction", "distillation"} {
		t.Run(operation, func(t *testing.T) {
			h, sid := newMemoryAuxiliaryTestHandler(t, "claude")
			var logs strings.Builder
			h.vlog = func(format string, args ...interface{}) {
				fmt.Fprintf(&logs, format, args...)
			}
			if err := h.store.UpdateSessionStatus(sid, store.StateWorking); err != nil {
				t.Fatal(err)
			}
			if err := h.store.AddConversationTurn(sid, "user", "Preserve this task"); err != nil {
				t.Fatal(err)
			}
			memoryDB := filepath.Join(h.dataDir, "sessions", sid, "memory.db")
			if err := os.MkdirAll(filepath.Dir(memoryDB), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(memoryDB, []byte("not sqlite"), 0600); err != nil {
				t.Fatal(err)
			}
			// A nil runner ensures failed preparation never invokes an agent.
			if operation == "compaction" {
				h.runCompaction(sid, "Continue", "", "")
			} else {
				h.runDistillation(sid)
			}
			sess, err := h.store.GetSession(sid)
			if err != nil || sess.Status != store.StateIdle || sess.AgentSessionID != "original-agent" {
				t.Fatalf("session not recovered intact: %+v, %v", sess, err)
			}
			if !strings.Contains(logs.String(), "cannot prepare memory") {
				t.Fatalf("missing failure log: %s", logs.String())
			}
			turns, err := h.store.GetConversation(sid)
			if err != nil {
				t.Fatal(err)
			}
			for _, turn := range turns {
				if turn.Role == "compaction" {
					t.Fatal("failed preparation marked history compacted")
				}
			}
		})
	}
}

func TestDistillationTaskUsesSystemContract(t *testing.T) {
	if !strings.Contains(distillPrompt, "system memory contract") {
		t.Fatal("distillation task must use the shared memory contract")
	}

	for _, duplicate := range []string{"README.md", "4000", "2000", notifyUserSystemPromptSuffix} {
		if strings.Contains(distillPrompt, duplicate) {
			t.Fatalf("unexpected instructions in task: %s", duplicate)
		}

	}
}

func TestCleanTranscriptPreservesPromptRoles(t *testing.T) {
	turns := []*store.ConversationTurn{
		{Role: "scheduled", Content: "wake up"},
		{Role: "callback", Content: "child report", SourceName: "worker"},
		{Role: "assistant", Content: "done"},
	}
	got := cleanTranscript(turns)
	for _, want := range []string{"SCHEDULED: wake up", "CALLBACK (worker): child report", "ASSISTANT: done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript lost role metadata %q: %s", want, got)
		}
	}
}

func TestDistillationChunksAreTurnAlignedAndBounded(t *testing.T) {
	turns := []*store.ConversationTurn{
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	}

	chunks := distillationChunks(turns, distillationPromptOverhead()+len("USER: one\n\nASSISTANT: two\n\n")+1)
	if len(chunks) != 2 || !strings.Contains(chunks[0], "ASSISTANT: two") || !strings.Contains(chunks[1], "USER: three") {
		t.Fatalf("unexpected turn-aligned chunks: %#v", chunks)
	}
}

func TestDistillationChunksExcludeOperationTurns(t *testing.T) {
	turns := []*store.ConversationTurn{
		{ID: 1, Role: "user", Content: "keep"},
		{ID: 2, Role: "compaction", Content: "do not feed back"},
		{ID: 3, Role: "distillation_partial", Content: "do not feed back"},
		{ID: 4, Role: "assistant", Content: "also keep"},
	}
	got := strings.Join(distillationChunks(turns, distillationPromptOverhead()+1024), "")
	if strings.Contains(got, "do not feed back") || !strings.Contains(got, "keep") {
		t.Fatalf("operation turns entered transcript: %q", got)
	}
}

func TestTurnsThroughSnapshotExcludesLaterTurns(t *testing.T) {
	turns := []*store.ConversationTurn{
		{ID: 1, Role: "user", Content: "old"},
		{ID: 2, Role: "assistant", Content: "old reply"},
		{ID: 3, Role: "user", Content: "new"},
	}

	got := cleanTranscript(turnsThroughSnapshot(turns, 2))
	if strings.Contains(got, "new") || !strings.Contains(got, "old") {
		t.Fatalf("snapshot watermark not enforced: %q", got)
	}
}

func TestDistillationChunksSplitUTF8WithinByteBudget(t *testing.T) {
	const payloadCap = 96
	chunks := distillationChunks([]*store.ConversationTurn{
		{Role: "user", Content: strings.Repeat("界", 100)},
	}, distillationPromptOverhead()+payloadCap)
	if len(chunks) < 2 {
		t.Fatal("oversized UTF-8 turn was not split")
	}
	for _, chunk := range chunks {
		if len(fmt.Sprintf(distillPrompt, chunk)) > distillationPromptOverhead()+payloadCap {
			t.Fatalf("chunk exceeds byte budget: %d", len(chunk))
		}
		if !utf8.ValidString(chunk) {
			t.Fatal("chunk split invalid UTF-8")
		}
	}
}

func TestDistillationPromptCapIncludesWrapperASCIIAndUTF8(t *testing.T) {
	const cap = 512
	for _, content := range []string{"ascii " + strings.Repeat("x", 800), strings.Repeat("界", 800)} {
		chunks := distillationChunks([]*store.ConversationTurn{{Role: "user", Content: content}}, cap)
		if len(chunks) == 0 {
			t.Fatal("expected chunks")
		}
		for _, chunk := range chunks {
			if got := len(fmt.Sprintf(distillPrompt, chunk)); got > cap {
				t.Fatalf("prompt exceeds final cap for %q: %d > %d", content[:5], got, cap)
			}
			if !utf8.ValidString(chunk) {
				t.Fatal("chunk is invalid UTF-8")
			}
		}
	}
}

func TestDistillationChunksRejectBudgetThatCannotFitPromptWrapper(t *testing.T) {
	turns := []*store.ConversationTurn{{Role: "user", Content: "source"}}
	for _, cap := range []int{0, distillationPromptOverhead() - 1} {
		if chunks := distillationChunks(turns, cap); len(chunks) != 0 {
			t.Fatalf("prompt cap %d unexpectedly generated chunks: %#v", cap, chunks)
		}
	}
}

func TestDistillationSnapshotWatermarkUsesSourceTurns(t *testing.T) {
	turns := []*store.ConversationTurn{
		{ID: 10, Role: "user", Content: "source"},
		{ID: 11, Role: "system", Content: "distillation_partial_failed"},
		{ID: 12, Role: "system", Content: "distillation_completed"},
	}
	if got := distillationSnapshotMaxID(turns); got != 10 {
		t.Fatalf("outcome turns moved snapshot watermark: got %d", got)
	}
	turns = append(turns, &store.ConversationTurn{ID: 13, Role: "user", Content: "new source"})
	if got := distillationSnapshotMaxID(turns); got != 13 {
		t.Fatalf("new source turn did not start a snapshot: got %d", got)
	}
}

func TestDistillationRetryResumesFailedChunkAfterOutcomeAppend(t *testing.T) {
	h, sid := newMemoryAuxiliaryTestHandler(t, "claude", envelope.GadgetCapabilities{Memory: true})
	payloads := []string{
		"first-" + strings.Repeat("A", 70000),
		"second-" + strings.Repeat("B", 70000),
	}
	for _, content := range payloads {
		if err := h.store.AddConversationTurn(sid, "user", content); err != nil {
			t.Fatal(err)
		}
	}
	sourceTurns, err := h.store.GetConversation(sid)
	if err != nil {
		t.Fatal(err)
	}
	sourceSnapshot := distillationSnapshotMaxID(sourceTurns)

	var prompts []string
	calls := 0
	var logs strings.Builder
	h.vlog = func(format string, args ...interface{}) { fmt.Fprintf(&logs, format+"\n", args...) }
	h.runAgentFunc = func(_ context.Context, p agent.RunParams) (*agent.Result, error) {
		calls++
		prompts = append(prompts, p.Prompt)
		if calls == 2 {
			return nil, context.Canceled
		}
		return &agent.Result{Text: "recorded"}, nil
	}
	h.runDistillation(sid)
	if calls < 2 {
		t.Fatalf("expected a middle chunk failure, got %d calls", calls)
	}
	turns, err := h.store.GetConversation(sid)
	if err != nil {
		t.Fatal(err)
	}
	if turns[len(turns)-1].Content != "distillation_partial_failed" {
		t.Fatalf("missing failed outcome: %+v", turns)
	}
	progress, err := h.store.GetDistillationProgress(sid, sourceSnapshot, "claude||||false")
	if err != nil {
		t.Fatal(err)
	}
	if progress == nil || progress.SnapshotMaxTurnID != sourceSnapshot || progress.NextChunk != 1 {
		t.Fatalf("failed run moved or lost source snapshot progress: %+v", progress)
	}

	firstPrompt := prompts[0]
	failedPrompt := prompts[1]
	h.runDistillation(sid)
	if len(prompts) <= 2 {
		t.Fatalf("retry did not run an incomplete chunk: calls=%d", len(prompts))
	}
	if prompts[2] != failedPrompt || prompts[2] == firstPrompt {
		t.Fatalf("retry did not start at failed chunk: prompt order %d, %d, %d", 0, 1, 2)
	}
	turns, err = h.store.GetConversation(sid)
	if err != nil {
		t.Fatal(err)
	}
	var completed, partial int
	for _, turn := range turns {
		if turn.Role != "system" {
			continue
		}
		switch turn.Content {
		case "distillation_partial_failed":
			partial++
		case "distillation_completed":
			completed++
		}
	}
	if partial != 1 || completed != 1 {
		t.Fatalf("unexpected distillation outcomes: partial=%d completed=%d calls=%d logs=%s", partial, completed, calls, logs.String())
	}
}
