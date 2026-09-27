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

func TestOpenCodeFailureLogsCategoryWithoutProviderContent(t *testing.T) {
	h, _, sessionID := newOperationRepairHandler(t)
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
	h.runAgent(sessionID, agent.RunParams{SessionID: sessionID, Agent: "opencode", Prompt: "secret prompt content"})
	got := logs.String()
	for _, field := range []string{"session=" + sessionID, "category=rate_limit", "status=429", "resumed=true", "events=3", "stderr_bytes=42", "last_event=error"} {
		if !strings.Contains(got, field) {
			t.Errorf("failure log missing %s: %q", field, got)
		}
	}
	if strings.Contains(got, "secret prompt content") {
		t.Fatalf("failure log leaked content: %q", got)
	}
	if !isSessionNotFoundErr(&agent.OpenCodeFailure{Cause: errors.New("exit status 1"), Category: "session_not_found"}) {
		t.Fatal("lost OpenCode session-not-found recovery")
	}
}
