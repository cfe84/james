package agent

import (
	"reflect"
	"strings"
	"testing"
)

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
