package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
			// Malformed legacy storage would fail migration if disabled memory were touched.
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
			if err := os.MkdirAll(filepath.Join(h.dataDir, "sessions", sid, "memory", "README.md"), 0700); err != nil {
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
