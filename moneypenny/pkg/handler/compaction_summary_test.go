package handler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

type compactionNotificationCapture struct {
	ch chan []byte
}

func (c *compactionNotificationCapture) Write(b []byte) (int, error) {
	c.ch <- append([]byte(nil), b...)
	return len(b), nil
}

func readCompactionNotifications(c *compactionNotificationCapture, n int) []envelope.Notification {
	out := make([]envelope.Notification, 0, n)
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case raw := <-c.ch:
			var notification envelope.Notification
			if json.Unmarshal(raw, &notification) == nil {
				out = append(out, notification)
			}
		case <-deadline:
			return out
		}
	}
	return out
}

func TestCompactionRejectsEmptyHandoffSummary(t *testing.T) {
	for _, summary := range []string{"", " \t\n"} {
		if validCompactionSummary(summary) {
			t.Fatalf("empty summary %q was accepted", summary)
		}

	}
	if !validCompactionSummary("Preserve this work") {
		t.Fatal("non-empty summary was rejected")
	}
}

func TestShouldCompactUsesPersistedThresholdOnlyForCustomMode(t *testing.T) {
	h := &Handler{}
	for _, tc := range []struct {
		name      string
		mode      string
		threshold int
		tokens    int
		want      bool
	}{
		{"custom below 10000", store.CompactionCustom, envelope.MinCompactionThresholdTokens, 9999, false},
		{"custom at 10000", store.CompactionCustom, envelope.MinCompactionThresholdTokens, 10000, true},
		{"custom below 900000", store.CompactionCustom, envelope.MaxCompactionThresholdTokens, 899999, false},
		{"custom at 900000", store.CompactionCustom, envelope.MaxCompactionThresholdTokens, 900000, true},
		{"agent ignores threshold", store.CompactionAgent, envelope.MinCompactionThresholdTokens, 900000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.shouldCompact(&store.Session{
				CompactionMode: tc.mode, CompactionThresholdTokens: tc.threshold,
				ContextTokens: tc.tokens,
			}); got != tc.want {
				t.Fatalf("shouldCompact() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCompactionThresholdHandlerValidationAndDetails(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	for _, threshold := range []int{envelope.MinCompactionThresholdTokens, envelope.MaxCompactionThresholdTokens} {
		value := threshold
		data, err := json.Marshal(envelope.UpdateSessionData{
			SessionID: sid, CompactionThresholdTokens: &value,
		})
		if err != nil {
			t.Fatal(err)
		}
		resp := h.updateSession(context.Background(), &envelope.Command{RequestID: "update", Data: data})
		if resp.Status != envelope.StatusSuccess {
			t.Fatalf("update threshold %d failed: %#v", threshold, resp)
		}
		detailResp := h.getSession(context.Background(), &envelope.Command{
			RequestID: "get", Data: mustJSON(envelope.SessionIDData{SessionID: sid}),
		})
		if detailResp.Status != envelope.StatusSuccess {
			t.Fatalf("get session failed: %#v", detailResp)
		}
		detail, ok := detailResp.Data.(envelope.SessionDetail)
		if !ok {
			t.Fatalf("get session data type = %T", detailResp.Data)
		}
		if detail.CompactionThresholdTokens != threshold {
			t.Fatalf("detail threshold = %d, want %d", detail.CompactionThresholdTokens, threshold)
		}
	}
	for _, threshold := range []int{envelope.MinCompactionThresholdTokens - 1, envelope.MaxCompactionThresholdTokens + 1} {
		value := threshold
		data, err := json.Marshal(envelope.UpdateSessionData{SessionID: sid, CompactionThresholdTokens: &value})
		if err != nil {
			t.Fatal(err)
		}
		resp := h.updateSession(context.Background(), &envelope.Command{RequestID: "invalid", Data: data})
		if resp.Status != envelope.StatusError || resp.ErrorCode != envelope.ErrInvalidRequest {
			t.Fatalf("update threshold %d response = %#v, want invalid request", threshold, resp)
		}
	}
	if sess, err := st.GetSession(sid); err != nil || sess.CompactionThresholdTokens != envelope.MaxCompactionThresholdTokens {
		t.Fatalf("invalid update changed persisted threshold: sess=%+v err=%v", sess, err)
	}
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func newCompactionTestHandler(t *testing.T) (*Handler, *store.Store, string) {
	return newCompactionTestHandlerForAgent(t, "claude")
}

func newCompactionTestHandlerForAgent(t *testing.T, agentName string) (*Handler, *store.Store, string) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	caps, _ := json.Marshal(envelope.GadgetCapabilities{})
	if err := st.CreateSession(&store.Session{SessionID: sid, Name: "test", Agent: agentName, AgentSessionID: "old", GadgetCapabilities: string(caps)}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateSessionStatus(sid, store.StateWorking)
	h := &Handler{store: st, dataDir: t.TempDir(), vlog: func(string, ...interface{}) {}}
	t.Cleanup(func() { st.Close() })
	return h, st, sid
}

func TestManualCompactionBootstrapFailureIsDurableAndKeepsOldID(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	calls := 0
	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		calls++
		if calls == 1 {
			return &agent.Result{Text: "handoff"}, nil
		}
		return nil, context.Canceled
	}
	h.runCompactionWithParams(sid, "manual prompt", "", "", compactionManual, agent.RunParams{})
	sess, _ := st.GetSession(sid)
	if sess.AgentSessionID != "old" || sess.Status != store.StateIdle {
		t.Fatalf("bootstrap failure replaced session: %+v", sess)
	}
	turns, _ := st.GetConversation(sid)
	if len(turns) != 1 || turns[0].Content != "compaction_bootstrap_failed" {
		t.Fatalf("unexpected durable outcome: %+v", turns)
	}
}

func TestManualCompactionSuccessDoesNotCreateAssistantTurn(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	calls := 0
	h.runAgentFunc = func(_ context.Context, p agent.RunParams) (*agent.Result, error) {
		calls++
		if calls == 1 && !p.Resume {
			t.Fatal("summary did not resume old agent")
		}
		if calls > 2 {
			t.Fatal("manual compaction resumed a user-facing continuation")
		}
		return &agent.Result{Text: "handoff"}, nil
	}
	h.runCompactionWithParams(sid, "manual prompt", "", "", compactionManual, agent.RunParams{})
	sess, _ := st.GetSession(sid)
	if sess.AgentSessionID == "old" || sess.Status != store.StateIdle {
		t.Fatalf("handoff did not complete: %+v", sess)
	}
	turns, _ := st.GetConversation(sid)
	for _, turn := range turns {
		if turn.Role == "assistant" || strings.Contains(turn.Content, "Await") {
			t.Fatalf("manual compaction created meaningless turn: %+v", turns)
		}
	}
}

func TestCompactionSummaryRunnerErrorAndEmptyAreSafeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		res        *agent.Result
		err        error
	}{
		{"runner error", "compaction_summary_runner_failed", nil, context.Canceled},
		{"empty summary", "compaction_summary_empty", &agent.Result{Text: " \n"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st, sid := newCompactionTestHandler(t)
			h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) { return tc.res, tc.err }
			h.runCompaction(sid, "next", "", "")
			sess, _ := st.GetSession(sid)
			if sess.AgentSessionID != "old" || sess.Status != store.StateIdle {
				t.Fatalf("failed summary changed session: %+v", sess)
			}
			turns, _ := st.GetConversation(sid)
			if len(turns) != 1 || turns[0].Content != tc.want {
				t.Fatalf("unexpected outcome: %+v", turns)
			}
		})
	}
}

func TestCompactionSuccessfulBootstrapCommitResumePreservesMetadata(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	var calls []agent.RunParams
	h.runAgentFunc = func(_ context.Context, p agent.RunParams) (*agent.Result, error) {
		calls = append(calls, p)
		return &agent.Result{Text: "handoff"}, nil
	}
	h.runCompactionWithParams(sid, "next", "model-x", "high", compactionContinue, agent.RunParams{
		ContextTier: "long_context", Attachments: []string{"/a.txt"}, ReplyChannelID: 42, MarkReady: true,
	})
	if len(calls) != 3 {
		t.Fatalf("runner calls = %d, want summary/bootstrap/resume", len(calls))
	}
	if !calls[0].Resume || !calls[0].NoPersistTurns {
		t.Fatalf("summary params invalid: %+v", calls[0])
	}
	if calls[1].Resume || !calls[1].NoPersistTurns || calls[1].ReplyChannelID != 0 || calls[1].MarkReady {
		t.Fatalf("bootstrap leaked continuation metadata: %+v", calls[1])
	}
	if !calls[2].Resume || calls[2].Prompt != "next" || calls[2].ReplyChannelID != 42 ||
		!calls[2].MarkReady || len(calls[2].Attachments) != 1 || calls[2].ContextTier != "long_context" {
		t.Fatalf("continuation metadata missing: %+v", calls[2])
	}
	sess, _ := st.GetSession(sid)
	if sess.AgentSessionID == "old" || sess.ContextWindow != 200000 {
		t.Fatalf("handoff metadata not committed: %+v", sess)
	}
}

func TestOpenCodeCompactionUsesBootstrapSessionIDForContinuation(t *testing.T) {
	h, st, sid := newCompactionTestHandlerForAgent(t, "opencode")
	var calls []agent.RunParams
	h.runAgentFunc = func(_ context.Context, p agent.RunParams) (*agent.Result, error) {
		calls = append(calls, p)
		switch len(calls) {
		case 1:
			return &agent.Result{Text: "handoff"}, nil
		case 2:
			return &agent.Result{AgentSessionID: "ses_bootstrap"}, nil
		default:
			return &agent.Result{Text: "continued", AgentSessionID: "ses_bootstrap"}, nil
		}
	}

	h.runCompactionWithParams(sid, "next", "", "", compactionContinue, agent.RunParams{})

	if len(calls) != 3 {
		t.Fatalf("runner calls = %d, want summary/bootstrap/continuation", len(calls))
	}
	if !calls[2].Resume || calls[2].AgentSessionID != "ses_bootstrap" {
		t.Fatalf("continuation did not resume OpenCode bootstrap session: %+v", calls[2])
	}
	sess, _ := st.GetSession(sid)
	if sess.AgentSessionID != "ses_bootstrap" {
		t.Fatalf("committed OpenCode session id = %q, want ses_bootstrap", sess.AgentSessionID)
	}
}

func TestCompactionAutomaticPromptMatchingManualTextContinues(t *testing.T) {
	h, _, sid := newCompactionTestHandler(t)
	var calls []agent.RunParams
	h.runAgentFunc = func(_ context.Context, p agent.RunParams) (*agent.Result, error) {
		calls = append(calls, p)
		return &agent.Result{Text: "handoff"}, nil
	}

	h.runCompactionWithParams(sid, "Await next instructions.", "", "", compactionContinue, agent.RunParams{})

	if len(calls) != 3 {
		t.Fatalf("automatic prompt matching manual text stopped after %d calls", len(calls))
	}
	if calls[2].Prompt != "Await next instructions." {
		t.Fatalf("continuation prompt = %q, want exact automatic prompt", calls[2].Prompt)
	}
}

func TestCompactionBootstrapWithoutSessionIDDoesNotCommitOrContinue(t *testing.T) {
	h, st, sid := newCompactionTestHandlerForAgent(t, "opencode")
	calls := 0
	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		calls++
		if calls == 1 {
			return &agent.Result{Text: "handoff"}, nil
		}
		return &agent.Result{Text: "bootstrapped"}, nil
	}

	h.runCompactionWithParams(sid, "next", "", "", compactionContinue, agent.RunParams{})

	sess, _ := st.GetSession(sid)
	if calls != 2 {
		t.Fatalf("continuation started after unresolved bootstrap id: %d calls", calls)
	}
	if sess.AgentSessionID != "old" || sess.Status != store.StateIdle {
		t.Fatalf("unresolved bootstrap changed session: %+v", sess)
	}
	turns, _ := st.GetConversation(sid)
	if len(turns) != 1 || turns[0].Content != "compaction_bootstrap_missing_id" {
		t.Fatalf("unexpected unresolved bootstrap outcome: %+v", turns)
	}
}

func TestCompactionPostCommitContinuationFailureKeepsNewID(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	calls := 0
	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		calls++
		if calls < 3 {
			return &agent.Result{Text: "handoff"}, nil
		}
		return nil, context.Canceled
	}
	h.runCompactionWithParams(sid, "next", "", "", compactionContinue, agent.RunParams{})
	sess, _ := st.GetSession(sid)
	if sess.AgentSessionID == "old" || sess.Status != store.StateIdle {
		t.Fatalf("post-commit failure changed session incorrectly: %+v", sess)
	}
	turns, _ := st.GetConversation(sid)
	found := false
	for _, turn := range turns {
		if turn.Content == "agent_run_failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing safe post-commit outcome: %+v", turns)
	}
}

func TestCompactionCommitFailureDoesNotStartRealContinuation(t *testing.T) {
	h, st, sid := newCompactionTestHandler(t)
	calls := 0
	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		calls++
		if calls == 2 {
			st.Close()
		}
		return &agent.Result{Text: "handoff"}, nil
	}
	h.runCompactionWithParams(sid, "next", "", "", compactionContinue, agent.RunParams{})
	if calls != 2 {
		t.Fatalf("real continuation started after commit failure: %d calls", calls)
	}
}

func TestCompactionNotificationsUseOutcomeReason(t *testing.T) {
	t.Run("manual success", func(t *testing.T) {
		h, _, sid := newCompactionTestHandler(t)
		capture := &compactionNotificationCapture{ch: make(chan []byte, 8)}
		h.notifyWriter = envelope.NewNotificationWriter(capture)
		h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
			return &agent.Result{Text: "handoff"}, nil
		}
		h.runCompactionWithParams(sid, "manual prompt", "", "", compactionManual, agent.RunParams{})
		for _, notification := range readCompactionNotifications(capture, 2) {
			data, ok := notification.Data.(map[string]interface{})
			if ok && data["reason"] == "compaction_failed" {
				t.Fatalf("successful compaction reported failure: %+v", notification)
			}
		}
	})

	t.Run("precommit failure records once", func(t *testing.T) {
		h, st, sid := newCompactionTestHandler(t)
		capture := &compactionNotificationCapture{ch: make(chan []byte, 8)}
		h.notifyWriter = envelope.NewNotificationWriter(capture)
		h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
			return nil, context.Canceled
		}
		h.runCompaction(sid, "next", "", "")
		turns, err := st.GetConversation(sid)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, turn := range turns {
			if turn.Content == "compaction_summary_runner_failed" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("precommit failure outcome count = %d, want 1", count)
		}
		for _, notification := range readCompactionNotifications(capture, 2) {
			if data, ok := notification.Data.(map[string]interface{}); ok && data["reason"] != "compaction_failed" {
				t.Fatalf("precommit notification reason = %v", data["reason"])
			}
		}
	})
}
