package agent

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestOpenCodeStreamFailureHelper(t *testing.T) {
	mode := os.Getenv("JAMES_TEST_OPENCODE_FAILURE")
	if mode == "" {
		return
	}
	switch mode {
	case "nested":
		fmt.Fprintln(os.Stdout, `{"type":"error","sessionID":"ses_test","error":{"name":"APIError","data":{"statusCode":429,"message":"rate limit for secret prompt"}}}`)
	case "session":
		fmt.Fprintln(os.Stdout, `{"type":"error","error":{"name":"UnknownError","data":{"message":"No session, task, or name matched: secret"}}}`)
	}
	fmt.Fprintln(os.Stderr, "secret stderr content")
	os.Exit(1)
}

func TestOpenCodeFailureDiagnosticsExcludeContent(t *testing.T) {
	for _, tc := range []struct {
		mode, category, name, sessionID, lastEvent string
		status, events                             int
	}{
		{"nested", "rate_limit", "APIError", "ses_test", "error", 429, 1},
		{"session", "session_not_found", "UnknownError", "", "error", 0, 1},
		{"stderr_only", "unknown", "none", "", "none", 0, 0},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestOpenCodeStreamFailureHelper")
			cmd.Env = append(os.Environ(), "JAMES_TEST_OPENCODE_FAILURE="+tc.mode)
			stderr := newBoundedBuffer(maxCapturedStderrBytes)
			cmd.Stderr = stderr
			runner := New(log.New(io.Discard, "", 0))
			result, err := runner.runOpenCodeStreaming(cmd, newActivityBuffer(30), "test-session", stderr, true, true)
			var failure *OpenCodeFailure
			if !errors.As(err, &failure) {
				t.Fatalf("failure type = %T, want OpenCodeFailure: %v", err, err)
			}
			if result.AgentSessionID != tc.sessionID || !failure.Resumed || failure.HasSessionID != (tc.sessionID != "") ||
				failure.Category != tc.category || failure.ErrorName != tc.name || failure.StatusCode != tc.status ||
				failure.Events != tc.events || failure.StderrBytes == 0 || failure.LastEventType != tc.lastEvent {
				t.Fatalf("result = %+v, diagnostic = %+v", result, failure)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "rate limit for") {
				t.Fatalf("diagnostic leaked content: %v", err)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("lost process exit status: %v", err)
			}
		})
	}
}

func TestOpenCodeErrorCategories(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		status        int
		category      string
	}{
		{"UnknownError", "No session, task, or name matched", 0, "session_not_found"},
		{"APIError", "invalid key", 401, "authentication"},
		{"APIError", "retry", 429, "rate_limit"},
		{"APIError", "too many tokens for context window", 400, "context_limit"},
		{"APIError", "service unavailable", 503, "provider_unavailable"},
		{"UnknownError", "connection reset", 0, "network"},
		{"UnknownError", "unknown model", 0, "model_unavailable"},
		{"UnknownError", "secret prompt content", 0, "unknown"},
	} {
		if got := openCodeErrorCategory(tc.name, tc.message, tc.status); got != tc.category {
			t.Errorf("category(%q, %d) = %q, want %q", tc.message, tc.status, got, tc.category)
		}
	}
}

func TestBuildOpenCodeArgs(t *testing.T) {
	inv := buildOpenCodeArgs(RunParams{
		Agent:          "opencode",
		AgentSessionID: "ses_existing",
		Resume:         true,
		Model:          "azure/gpt-5.4",
		Effort:         "high",
		Yolo:           true,
		SystemPrompt:   "Be concise.",
		Prompt:         "Inspect this.",
		Attachments:    []string{"/tmp/one.txt", "/tmp/two.png"},
	})
	want := []string{
		"run", "--format", "json", "--session", "ses_existing",
		"--model", "azure/gpt-5.4", "--variant", "high", "--auto",
		"--file", "/tmp/one.txt", "--file", "/tmp/two.png",
		"Instructions for this task:\nBe concise.\n\n---\n\nInspect this.",
	}
	if !reflect.DeepEqual(inv.args, want) {
		t.Errorf("args = %#v, want %#v", inv.args, want)
	}
}

func TestBuildOpenCodeArgsCreatesWithoutSession(t *testing.T) {
	inv := buildOpenCodeArgs(RunParams{
		Agent:          "opencode",
		AgentSessionID: "ses_stale",
		Prompt:         "Hello",
	})
	for _, arg := range inv.args {
		if arg == "--session" || arg == "ses_stale" {
			t.Errorf("fresh invocation must not pass an OpenCode session id: %v", inv.args)
		}
	}
	if got := inv.args[len(inv.args)-1]; got != "Hello" {
		t.Errorf("prompt = %q, want Hello", got)
	}
}

func TestOpenCodeSystemPrompt(t *testing.T) {
	got := withOpenCodeSystemPrompt(RunParams{SystemPrompt: "Rules", Prompt: "Task"})
	if !strings.Contains(got, "Rules") || !strings.HasSuffix(got, "Task") {
		t.Errorf("combined prompt = %q", got)
	}
}
