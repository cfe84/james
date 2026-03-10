package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Result holds the parsed result from a claude invocation.
type Result struct {
	Text string // The text response extracted from Claude's JSON output
}

// ActivityEvent is a simplified representation of what the agent is doing right now.
type ActivityEvent struct {
	Type      string `json:"type"`      // "thinking", "tool_use", "text"
	Summary   string `json:"summary"`   // short description
	Timestamp string `json:"timestamp"` // RFC3339
}

// activityBuffer is a ring buffer of recent activity events for a session.
type activityBuffer struct {
	mu     sync.Mutex
	events []ActivityEvent
	max    int
}

func newActivityBuffer(max int) *activityBuffer {
	return &activityBuffer{max: max, events: make([]ActivityEvent, 0, max)}
}

func (ab *activityBuffer) add(ev ActivityEvent) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if len(ab.events) >= ab.max {
		copy(ab.events, ab.events[1:])
		ab.events = ab.events[:ab.max-1]
	}
	ab.events = append(ab.events, ev)
}

func (ab *activityBuffer) snapshot() []ActivityEvent {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	result := make([]ActivityEvent, len(ab.events))
	copy(result, ab.events)
	return result
}

// RunParams contains parameters for running an agent.
type RunParams struct {
	SessionID    string
	Agent        string // "claude" for now
	Prompt       string
	SystemPrompt string // only used on first invocation
	Model        string // model override (e.g. "sonnet", "opus")
	Yolo         bool
	Path         string // working directory for the agent
	Resume       bool   // true for continue_session
}

// Runner manages agent subprocess execution.
type Runner struct {
	mu       sync.Mutex
	procs    map[string]*exec.Cmd      // sessionID -> running process
	activity map[string]*activityBuffer // sessionID -> recent activity
	vlog     *log.Logger
}

// New creates a new Runner.
func New(vlog *log.Logger) *Runner {
	return &Runner{
		procs:    make(map[string]*exec.Cmd),
		activity: make(map[string]*activityBuffer),
		vlog:     vlog,
	}
}

// GetActivity returns recent activity events for a session.
func (r *Runner) GetActivity(sessionID string) []ActivityEvent {
	r.mu.Lock()
	buf, ok := r.activity[sessionID]
	r.mu.Unlock()
	if !ok {
		return nil
	}
	return buf.snapshot()
}

// Run invokes an agent with the given parameters. It blocks until the agent completes.
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

	buf := newActivityBuffer(30)
	r.mu.Lock()
	r.procs[params.SessionID] = cmd
	r.activity[params.SessionID] = buf
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.procs, params.SessionID)
		delete(r.activity, params.SessionID)
		r.mu.Unlock()
	}()

	// For Claude agents, use stream-json and parse events line by line.
	if params.Agent != "copilot" {
		return r.runStreaming(cmd, buf)
	}

	// Copilot: blocking output.
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agent process failed: %w", err)
	}
	return &Result{Text: parseOutput(params.Agent, output)}, nil
}

// runStreaming runs a Claude agent with stream-json, parsing events into the activity buffer.
func (r *Runner) runStreaming(cmd *exec.Cmd, buf *activityBuffer) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			r.vlog.Printf("stream: unparseable line: %s", truncStr(line, 200))
			continue
		}

		evType, _ := event["type"].(string)
		now := time.Now().UTC().Format(time.RFC3339)
		r.vlog.Printf("stream: event type=%q", evType)

		switch evType {
		case "assistant":
			msg, _ := event["message"].(map[string]any)
			if msg == nil {
				r.vlog.Printf("stream: assistant event has no message field")
				continue
			}
			contentBlocks, _ := msg["content"].([]any)
			r.vlog.Printf("stream: assistant message with %d content blocks", len(contentBlocks))
			for _, block := range contentBlocks {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := b["type"].(string)
				r.vlog.Printf("stream: content block type=%q", blockType)
				switch blockType {
				case "thinking":
					thinking, _ := b["thinking"].(string)
					if thinking != "" {
						buf.add(ActivityEvent{Type: "thinking", Summary: truncStr(thinking, 150), Timestamp: now})
					}
				case "tool_use":
					buf.add(ActivityEvent{Type: "tool_use", Summary: toolSummary(b), Timestamp: now})
				case "text":
					text, _ := b["text"].(string)
					if text != "" {
						buf.add(ActivityEvent{Type: "text", Summary: truncStr(text, 150), Timestamp: now})
					}
				}
			}
		case "result":
			if r, ok := event["result"].(string); ok {
				resultText = r
			} else if r, ok := event["result"]; ok {
				b, _ := json.Marshal(r)
				resultText = string(b)
			}
		default:
			r.vlog.Printf("stream: unhandled event type=%q keys=%v", evType, mapKeys(event))
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("agent process failed: %w", err)
	}
	return &Result{Text: resultText}, nil
}

// toolSummary builds a short description of a tool_use block.
func toolSummary(b map[string]any) string {
	name, _ := b["name"].(string)
	inp, _ := b["input"].(map[string]any)
	if inp == nil {
		return name
	}
	switch name {
	case "Read", "Write", "Edit", "Glob":
		if p, ok := inp["file_path"].(string); ok {
			return name + " " + p
		}
		if p, ok := inp["pattern"].(string); ok {
			return name + " " + p
		}
	case "Grep":
		pat, _ := inp["pattern"].(string)
		return name + " " + truncStr(pat, 60)
	case "Bash":
		if c, ok := inp["command"].(string); ok {
			return name + " " + truncStr(c, 80)
		}
	case "Agent":
		if d, ok := inp["description"].(string); ok {
			return name + " " + d
		}
	}
	return name
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
			"--output-format", "stream-json",
			"--verbose", // required for stream-json
			"--resume", params.SessionID,
		}
	} else {
		args = []string{
			"--output-format", "stream-json",
			"--verbose",
			"--session-id", params.SessionID,
		}
	}
	if params.SystemPrompt != "" {
		args = append(args, "--system-prompt", params.SystemPrompt)
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-p", params.Prompt)
	return args
}

func buildCopilotArgs(params RunParams) []string {
	args := []string{
		"--resume", params.SessionID,
		"-s",
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}
	args = append(args, "-p", params.Prompt)
	return args
}

// parseOutput extracts text from agent output based on agent type.
func parseOutput(agentName string, output []byte) string {
	switch agentName {
	case "copilot":
		return strings.TrimSpace(string(output))
	default:
		return parseClaudeOutput(output)
	}
}

// Stop kills the subprocess for the given session.
func (r *Runner) Stop(sessionID string) error {
	r.mu.Lock()
	cmd, ok := r.procs[sessionID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("no running process for session %s", sessionID)
	}
	delete(r.procs, sessionID)
	r.mu.Unlock()
	return cmd.Process.Kill()
}

// IsRunning returns true if a subprocess is currently running for the session.
func (r *Runner) IsRunning(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.procs[sessionID]
	return ok
}

// parseClaudeOutput extracts the text response from Claude's JSON output (fallback).
func parseClaudeOutput(output []byte) string {
	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return ""
	}
	var single map[string]any
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		if result, ok := extractResult(single); ok {
			return result
		}
	}
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
	return raw
}

func extractResult(obj map[string]any) (string, bool) {
	result, ok := obj["result"]
	if !ok {
		return "", false
	}
	switch v := result.(type) {
	case string:
		return v, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v), true
		}
		return string(b), true
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
