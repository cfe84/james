package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	case "hang_after_stop":
		fmt.Fprintln(os.Stdout, `{"type":"text","sessionID":"ses_test","part":{"text":"Done"}}`)
		fmt.Fprintln(os.Stdout, `{"type":"step_finish","sessionID":"ses_test","part":{"reason":"stop"}}`)
		time.Sleep(10 * time.Second)
	case "tool_then_stop":
		fmt.Fprintln(os.Stdout, `{"type":"step_finish","sessionID":"ses_test","part":{"reason":"tool-calls"}}`)
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stdout, `{"type":"text","sessionID":"ses_test","part":{"text":"Completed after tool"}}`)
		os.Exit(0)
	case "no_reply":
		fmt.Fprintln(os.Stdout, `{"type":"step_finish","sessionID":"ses_test","part":{"reason":"stop"}}`)
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "secret stderr content")
	os.Exit(1)
}

func TestOpenCodeCompletedWithoutReplyFails(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestOpenCodeStreamFailureHelper")
	cmd.Env = append(os.Environ(), "JAMES_TEST_OPENCODE_FAILURE=no_reply")
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	_, err := runner.runOpenCodeStreaming(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, false, "")
	var failure *OpenCodeFailure
	if !errors.As(err, &failure) || failure.Category != "no_reply" {
		t.Fatalf("successful process without reply must be a diagnosed failure: %v", err)
	}
}

func TestOpenCodeExportRequiresCurrentCompletedReply(t *testing.T) {
	for _, tc := range []struct {
		name       string
		userOffset time.Duration
		finish     string
		complete   int64
		later      string
		want       bool
	}{
		{"current", time.Second, "stop", 1, "", true},
		{"old_user", -time.Hour, "stop", 1, "", false},
		{"unfinished", time.Second, "stop", 0, "", false},
		{"tool_step", time.Second, "tool-calls", 1, "", false},
		{"later_unfinished", time.Second, "stop", 1, `,{"info":{"id":"later","parentID":"user","role":"assistant","time":{"created":2}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			payload := fmt.Sprintf(`{"messages":[{"info":{"id":"user","role":"user","time":{"created":%d}}},{"info":{"id":"assistant","parentID":"user","role":"assistant","finish":%q,"time":{"completed":%d}},"parts":[{"type":"text","text":"secret answer","time":{"end":1}}]}%s]}`,
				start.Add(tc.userOffset).UnixMilli(), tc.finish, tc.complete, tc.later)
			script := filepath.Join(t.TempDir(), "export")
			if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '"+payload+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			reply, complete := openCodeExportReply(context.Background(), &exec.Cmd{Path: script}, "ses_test", start, time.Second)
			if complete != tc.want || (complete && reply.text != "secret answer") || (!complete && reply.text != "") {
				t.Fatalf("export recovery: complete=%t text_length=%d", complete, len(reply.text))
			}
		})
	}
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
			result, err := runner.runOpenCodeStreaming(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, true, "ses_existing")
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

func TestOpenCodeHungTerminalEventReturnsReply(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestOpenCodeStreamFailureHelper")
	cmd.Env = append(os.Environ(), "JAMES_TEST_OPENCODE_FAILURE=hang_after_stop")
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	watch := openCodeWatchConfig{interval: 10 * time.Millisecond, terminalGrace: 30 * time.Millisecond, exportAfter: time.Hour, exportTimeout: time.Second}
	start := time.Now()
	result, err := runner.runOpenCodeStreamingWithWatch(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, true, "", watch)
	if err != nil || result.Text != "Done" || result.OpenCodeRecovery != "terminal_event" || time.Since(start) > time.Second {
		t.Fatalf("hung CLI recovery: result=%+v err=%v duration=%v", result, err, time.Since(start))
	}
}

func TestOpenCodeNonterminalStepContinues(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestOpenCodeStreamFailureHelper")
	cmd.Env = append(os.Environ(), "JAMES_TEST_OPENCODE_FAILURE=tool_then_stop")
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	watch := openCodeWatchConfig{interval: 10 * time.Millisecond, terminalGrace: 100 * time.Millisecond, exportAfter: time.Hour, exportTimeout: time.Second}
	result, err := runner.runOpenCodeStreamingWithWatch(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, true, "", watch)
	if err != nil || result.Text != "Completed after tool" || result.OpenCodeRecovery != "" {
		t.Fatalf("nonterminal step ended prematurely: result=%+v err=%v", result, err)
	}
}

func TestOpenCodeCancellationDoesNotPublishReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestOpenCodeStreamFailureHelper")
	cmd.Env = append(os.Environ(), "JAMES_TEST_OPENCODE_FAILURE=hang_after_stop")
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	watch := openCodeWatchConfig{interval: 10 * time.Millisecond, terminalGrace: 300 * time.Millisecond, exportAfter: time.Hour, exportTimeout: time.Second}
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	result, err := runner.runOpenCodeStreamingWithWatch(ctx, cmd, newActivityBuffer(30), "test-session", stderr, true, true, "", watch)
	if !errors.Is(err, context.Canceled) || result.OpenCodeRecovery != "" {
		t.Fatalf("cancelled run published recovery: result=%+v err=%v", result, err)
	}
}

func TestOpenCodeHungWithoutEventsRecoversCompletedExport(t *testing.T) {
	script := filepath.Join(t.TempDir(), "opencode")
	source := `#!/bin/sh
if [ "$1" = export ]; then
  printf '%s\n' '{"messages":[{"info":{"id":"msg_user","role":"user","time":{"created":32503680000000}}},{"info":{"id":"msg_assistant","parentID":"msg_user","role":"assistant","finish":"stop","cost":0.02,"time":{"completed":32503680000100}},"parts":[{"type":"text","text":"Recovered","time":{"end":32503680000100}}]}]}'
else
  exec sleep 10
fi
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "run")
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	watch := openCodeWatchConfig{interval: 10 * time.Millisecond, terminalGrace: time.Second, exportAfter: 25 * time.Millisecond, exportTimeout: time.Second}
	start := time.Now()
	result, err := runner.runOpenCodeStreamingWithWatch(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, true, "ses_existing", watch)
	if err != nil || result.Text != "Recovered" || result.OpenCodeCost != 0.02 || result.OpenCodeRecovery != "session_export" || time.Since(start) > 2*time.Second {
		t.Fatalf("silent CLI recovery: result=%+v err=%v duration=%v", result, err, time.Since(start))
	}
}

func TestOpenCodeExitWithoutTextRecoversExport(t *testing.T) {
	script := filepath.Join(t.TempDir(), "opencode")
	source := `#!/bin/sh
if [ "$1" = export ]; then
  printf '%s\n' '{"messages":[{"info":{"id":"user","role":"user","time":{"created":32503680000000}}},{"info":{"id":"assistant","parentID":"user","role":"assistant","finish":"stop","cost":0.03,"time":{"completed":32503680000100}},"parts":[{"type":"text","text":"Saved answer","time":{"end":32503680000100}}]}]}'
else
  printf '%s\n' '{"type":"step_start","sessionID":"ses_test"}'
fi
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	stderr := newBoundedBuffer(maxCapturedStderrBytes)
	cmd := exec.Command(script, "run")
	cmd.Stderr = stderr
	runner := New(log.New(io.Discard, "", 0))
	result, err := runner.runOpenCodeStreaming(context.Background(), cmd, newActivityBuffer(30), "test-session", stderr, true, false, "")
	if err != nil || result.Text != "Saved answer" || result.OpenCodeCost != 0.03 || result.OpenCodeRecovery != "session_export" {
		t.Fatalf("exit-zero missing text recovery: result=%+v err=%v", result, err)
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
		"run", "--format", "json", "--thinking", "--session", "ses_existing",
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
