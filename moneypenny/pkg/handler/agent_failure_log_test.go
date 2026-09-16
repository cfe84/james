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
