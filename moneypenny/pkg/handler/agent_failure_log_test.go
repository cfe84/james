package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/store"
)

func TestRunAgentLogsSafeFailureSummaryWithoutVerboseLogging(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	var logs strings.Builder
	h.errorLog = func(format string, args ...interface{}) {
		fmt.Fprintf(&logs, format, args...)
	}

	h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		return nil, errors.New("agent process failed: exit status 1\nstderr:\nsecret prompt content")
	}

	h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "opencode", Prompt: "secret prompt content"})

	got := logs.String()
	if !strings.Contains(got, "session="+sessionID) || !strings.Contains(got, "agent=opencode") || !strings.Contains(got, "exit status 1") {
		t.Fatalf("failure log = %q", got)
	}
	if strings.Contains(got, "secret prompt content") {
		t.Fatalf("failure log leaked sensitive error detail: %q", got)
	}
	session, err := s.GetSession(sessionID)
	if err != nil || session.Status != store.StateIdle {
		t.Fatalf("session did not recover to idle: session=%#v err=%v", session, err)
	}
}

func TestRunAgentDoesNotMislabelAgentErrorsAsMemoryPreparation(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"model error", "agent process failed: exit status 1\nstderr:\nError: Model luna is not available. Check memory."},
		{"read error", "agent process failed: could not read agent output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, sessionID := newOperationRepairHandler(t)
			h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
				return nil, errors.New(tc.message)
			}
			h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "copilot"})
			turns, err := s.GetConversation(sessionID)
			if err != nil || len(turns) != 1 || !strings.HasPrefix(turns[0].Content, "Agent failed\n") ||
				strings.Contains(turns[0].Content, "memory preparation") || strings.Contains(turns[0].Content, tc.message) {
				t.Fatalf("incorrect failure label: turns=%v err=%v", turns, err)
			}
		})
	}
}

func TestOpenCodeFailureLogsCategoryWithoutProviderContent(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	var logs strings.Builder
	h.errorLog = func(format string, args ...interface{}) {
		fmt.Fprintf(&logs, format, args...)
	}

	failure := &agent.OpenCodeFailure{
		Cause: errors.New("exit status 1"), Category: "rate_limit", ErrorName: "APIError",
		StatusCode: 429, Events: 3, StderrBytes: 42, Resumed: true,
		HasSessionID: true, LastEventType: "error",
	}

	h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		return nil, failure
	}
	h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "opencode", Model: "openai/gpt-6-luna", Prompt: "secret prompt content"})
	got := logs.String()
	for _, field := range []string{"session=" + sessionID, "category=rate_limit", "status=429", "resumed=true", "events=3", "stderr_bytes=42", "last_event=error"} {
		if !strings.Contains(got, field) {
			t.Errorf("failure log missing %s: %q", field, got)
		}
	}
	if strings.Contains(got, "secret prompt content") {
		t.Fatalf("failure log leaked content: %q", got)
	}
	turns, err := s.GetConversation(sessionID)
	if err != nil || len(turns) != 1 || turns[0].Role != "system" {
		t.Fatalf("missing conversation failure: turns=%v err=%v", turns, err)
	}
	for _, text := range []string{"Agent failed: rate limit (HTTP 429)", "Agent: opencode", "Model: openai/gpt-6-luna", "Wait before retrying"} {
		if !strings.Contains(turns[0].Content, text) {
			t.Errorf("conversation failure missing %q: %q", text, turns[0].Content)
		}
	}
	if !isSessionNotFoundErr(&agent.OpenCodeFailure{Cause: errors.New("exit status 1"), Category: "session_not_found"}) {
		t.Fatal("lost OpenCode session-not-found recovery")
	}
}

func TestAgentFailureMessageUsesOnlySafeMetadata(t *testing.T) {
	for _, tc := range []struct {
		category string
		status   int
		title    string
		action   string
	}{
		{"authentication", 401, "authentication (HTTP 401)", "credentials"},
		{"authentication", 403, "authentication (HTTP 403)", "credentials"},
		{"context_limit", 0, "context limit", "Compact"},
		{"provider_unavailable", 503, "provider unavailable (HTTP 503)", "Retry later"},
		{"network", 0, "network error", "connectivity"},
		{"model_unavailable", 0, "model unavailable", "provider/model ID"},
		{"session_not_found", 0, "agent session unavailable", "recovery"},
		{"secret provider message", 0, "Agent failed\n", "diagnostics"},
	} {
		t.Run(tc.category+fmt.Sprint(tc.status), func(t *testing.T) {
			failure := &agent.OpenCodeFailure{
				Cause:    errors.New("secret credentials and provider response"),
				Category: tc.category, StatusCode: tc.status, ErrorName: "secret error name",
			}
			message := agentFailureMessage(agent.RunParams{Agent: "opencode", Model: "openai/gpt-6-luna"}, failure)
			if !strings.Contains(message, tc.title) || !strings.Contains(message, tc.action) ||
				strings.Contains(message, "secret") {
				t.Fatalf("unsafe or incomplete message: %q", message)
			}
		})
	}
}

func TestLostOpenCodeSessionPersistsReplacementID(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	calls := 0
	h.runAgentFunc = func(_ context.Context, params agent.RunParams) (*agent.Result, error) {
		calls++
		if calls == 1 {
			return nil, &agent.OpenCodeFailure{Cause: errors.New("missing session"), Category: "session_not_found"}
		}
		if params.Resume || params.AgentSessionID != "" {
			t.Fatalf("replacement must be a fresh OpenCode run: resume=%t id=%q", params.Resume, params.AgentSessionID)
		}
		return &agent.Result{Text: "Recovered", AgentSessionID: "ses_replacement"}, nil
	}
	h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "opencode", Resume: true, Prompt: "continue"})
	got, err := s.GetSession(sessionID)
	if err != nil || calls != 2 || got.AgentSessionID != "ses_replacement" || got.Status != store.StateIdle {
		t.Fatalf("replacement ID not persisted: calls=%d session=%+v err=%v", calls, got, err)
	}
}
func TestOpenCodeRecoveredReplyIsPersistedAndLoggedSafely(t *testing.T) {
	h, s, sessionID := newOperationRepairHandler(t)
	var logs strings.Builder
	h.errorLog = func(format string, args ...interface{}) {
		fmt.Fprintf(&logs, format, args...)
	}
	h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		return &agent.Result{Text: "Recovered answer", OpenCodeRecovery: "session_export"}, nil
	}
	h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "opencode", Prompt: "secret prompt"})
	turns, err := s.GetConversation(sessionID)
	if err != nil || len(turns) != 1 || turns[0].Role != "assistant" || turns[0].Content != "Recovered answer" {
		t.Fatalf("recovered answer not saved: turns=%v err=%v", turns, err)
	}
	if got := logs.String(); !strings.Contains(got, "source=session_export") ||
		!strings.Contains(got, "session="+sessionID) || strings.Contains(got, "secret") || strings.Contains(got, "Recovered answer") {
		t.Fatalf("recovery log must contain only safe metadata: %q", got)
	}
}
