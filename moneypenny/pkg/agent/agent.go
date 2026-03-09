package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Result holds the parsed result from a claude invocation.
type Result struct {
	Text string // The text response extracted from Claude's JSON output
}

// RunParams contains parameters for running an agent.
type RunParams struct {
	SessionID    string
	Agent        string // "claude" for now
	Prompt       string
	SystemPrompt string // only used on first invocation
	Yolo         bool
	Path         string // working directory for the agent
	Resume       bool   // true for continue_session
}

// Runner manages agent subprocess execution.
type Runner struct {
	mu        sync.Mutex
	processes map[string]*exec.Cmd // sessionID -> running process
	vlog      *log.Logger
}

// New creates a new Runner.
func New(vlog *log.Logger) *Runner {
	return &Runner{
		processes: make(map[string]*exec.Cmd),
		vlog:      vlog,
	}
}

// Run invokes an agent with the given parameters. It blocks until the agent completes.
// Returns the text response extracted from the agent's output.
//
// For create_session (no existing session), pass Resume=false.
// For continue_session, pass Resume=true to continue the conversation.
//
// The process is tracked so it can be killed via Stop().
func (r *Runner) Run(ctx context.Context, params RunParams) (*Result, error) {
	agentPath, err := exec.LookPath(params.Agent)
	if err != nil {
		return nil, fmt.Errorf("agent binary %q not found: %w", params.Agent, err)
	}

	args := buildArgs(params)

	cmd := exec.CommandContext(ctx, agentPath, args...)
	if params.Path != "" {
		cmd.Dir = params.Path
	}
	cmd.Env = append(os.Environ(), "HEM_SESSION_ID="+params.SessionID)
	cmd.Stderr = os.Stderr

	r.vlog.Printf("exec: %s %s", agentPath, strings.Join(args, " "))

	r.mu.Lock()
	r.processes[params.SessionID] = cmd
	r.mu.Unlock()

	output, err := cmd.Output()

	r.mu.Lock()
	delete(r.processes, params.SessionID)
	r.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("agent process failed: %w", err)
	}

	text := parseOutput(params.Agent, output)
	return &Result{Text: text}, nil
}

// buildArgs constructs the command-line arguments for the given agent.
func buildArgs(params RunParams) []string {
	switch params.Agent {
	case "copilot":
		return buildCopilotArgs(params)
	default:
		return buildClaudeArgs(params)
	}
}

func buildClaudeArgs(params RunParams) []string {
	var args []string
	if params.Resume {
		args = []string{
			"--output-format", "json",
			"--resume", params.SessionID,
		}
	} else {
		args = []string{
			"--output-format", "json",
			"--session-id", params.SessionID,
		}
		if params.SystemPrompt != "" {
			args = append(args, "--system-prompt", params.SystemPrompt)
		}
	}
	if params.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-p", params.Prompt)
	return args
}

func buildCopilotArgs(params RunParams) []string {
	// Copilot uses --resume for both new sessions (with a UUID) and continuing.
	args := []string{
		"--resume", params.SessionID,
		"-s", // silent mode: output only the agent response
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}
	args = append(args, "-p", params.Prompt)
	return args
}

// parseOutput extracts text from agent output based on agent type.
func parseOutput(agent string, output []byte) string {
	switch agent {
	case "copilot":
		return strings.TrimSpace(string(output))
	default:
		return parseClaudeOutput(output)
	}
}

// Stop kills the subprocess for the given session. Returns error if no process is running.
func (r *Runner) Stop(sessionID string) error {
	r.mu.Lock()
	cmd, ok := r.processes[sessionID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("no running process for session %s", sessionID)
	}
	delete(r.processes, sessionID)
	r.mu.Unlock()

	return cmd.Process.Kill()
}

// IsRunning returns true if a subprocess is currently running for the session.
func (r *Runner) IsRunning(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.processes[sessionID]
	return ok
}

// parseClaudeOutput extracts the text response from Claude's JSON output.
// Claude's JSON output has a "result" field containing the text.
// The output may contain multiple JSON lines. We want the last one
// that has a "result" field.
// If parsing fails, return the raw stdout as the text (graceful fallback).
func parseClaudeOutput(output []byte) string {
	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return ""
	}

	// Try to unmarshal the entire output as a single JSON object first.
	var single map[string]any
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		if result, ok := extractResult(single); ok {
			return result
		}
	}

	// If that fails, split by newlines and find the last object with a "result" field.
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil {
			if result, ok := extractResult(obj); ok {
				return result
			}
		}
	}

	// Graceful fallback: return raw output.
	return raw
}

// extractResult attempts to extract a text result from a parsed JSON object.
func extractResult(obj map[string]any) (string, bool) {
	result, ok := obj["result"]
	if !ok {
		return "", false
	}
	switch v := result.(type) {
	case string:
		return v, true
	default:
		// If result is not a string, marshal it back to JSON.
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v), true
		}
		return string(b), true
	}
}
