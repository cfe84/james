package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"james/moneypenny/pkg/envelope"
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
	Effort       string // reasoning effort level (e.g. "low", "medium", "high")
	Yolo         bool
	Path         string // working directory for the agent
	Resume       bool   // true for continue_session
}

// Runner manages agent subprocess execution.
type Runner struct {
	mu           sync.Mutex
	procs        map[string]*exec.Cmd      // sessionID -> running process
	activity     map[string]*activityBuffer // sessionID -> recent activity
	vlog         *log.Logger
	notifyWriter *envelope.NotificationWriter
}

// New creates a new Runner.
func New(vlog *log.Logger) *Runner {
	return &Runner{
		procs:    make(map[string]*exec.Cmd),
		activity: make(map[string]*activityBuffer),
		vlog:     vlog,
	}
}

// SetNotificationWriter sets the notification writer for sending real-time events.
func (r *Runner) SetNotificationWriter(nw *envelope.NotificationWriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notifyWriter = nw
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
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

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

	// Both Claude and Copilot use streaming JSON output.
	if params.Agent == "copilot" {
		return r.runCopilotStreaming(cmd, buf, params.SessionID, &stderrBuf)
	}
	return r.runStreaming(cmd, buf, params.SessionID, &stderrBuf)
}

// runStreaming runs a Claude agent with stream-json, parsing events into the activity buffer.
func (r *Runner) runStreaming(cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf *bytes.Buffer) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText string
	var lastRawEvent string // keep the last raw JSON line for error diagnostics
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lastRawEvent = line

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
			// Send activity notification after processing assistant event
			if r.notifyWriter != nil && len(contentBlocks) > 0 {
				snapshot := buf.snapshot()
				_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
					"events": snapshot,
				})
			}
		case "result":
			r.vlog.Printf("stream: result event: %s", truncStr(line, 500))
			if r, ok := event["result"].(string); ok {
				resultText = r
			} else if r, ok := event["result"]; ok {
				b, _ := json.Marshal(r)
				resultText = string(b)
			}
		case "error":
			r.vlog.Printf("stream: error event: %s", truncStr(line, 500))
			if errMsg, ok := event["error"].(string); ok {
				resultText = "Error: " + errMsg
			} else if errObj, ok := event["error"].(map[string]any); ok {
				if msg, ok := errObj["message"].(string); ok {
					resultText = "Error: " + msg
				}
			}
		default:
			r.vlog.Printf("stream: unhandled event type=%q keys=%v data=%s", evType, mapKeys(event), truncStr(line, 300))
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmtAgentErrorFull(err, stderrBuf, resultText, lastRawEvent)
	}
	return &Result{Text: resultText}, nil
}

// runCopilotStreaming runs a Copilot agent with --output-format json --stream on,
// parsing JSONL events into the activity buffer (same pattern as Claude streaming).
func (r *Runner) runCopilotStreaming(cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf *bytes.Buffer) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText string
	var lastRawEvent string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lastRawEvent = line

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			r.vlog.Printf("copilot stream: unparseable line: %s", truncStr(line, 200))
			continue
		}

		evType, _ := event["type"].(string)
		data, _ := event["data"].(map[string]any)
		now := time.Now().UTC().Format(time.RFC3339)

		switch evType {
		case "assistant.turn_start":
			buf.add(ActivityEvent{Type: "thinking", Summary: "thinking...", Timestamp: now})
			if r.notifyWriter != nil {
				_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
					"events": buf.snapshot(),
				})
			}

		case "assistant.message":
			if data != nil {
				content, _ := data["content"].(string)
				r.vlog.Printf("copilot stream: assistant.message content=%d bytes, toolRequests=%v",
					len(content), data["toolRequests"] != nil)
				if content != "" {
					resultText = content
					buf.add(ActivityEvent{Type: "text", Summary: truncStr(content, 150), Timestamp: now})
				}
				// Parse tool requests for activity.
				if toolReqs, ok := data["toolRequests"].([]any); ok {
					for _, tr := range toolReqs {
						trMap, ok := tr.(map[string]any)
						if !ok {
							continue
						}
						name, _ := trMap["name"].(string)
						if name == "" || name == "report_intent" {
							continue
						}
						summary := name
						if args, ok := trMap["arguments"].(map[string]any); ok {
							summary = copilotToolSummary(name, args)
						}
						buf.add(ActivityEvent{Type: "tool_use", Summary: summary, Timestamp: now})
					}
				}
				if r.notifyWriter != nil {
					_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
						"events": buf.snapshot(),
					})
				}
			}

		case "tool.execution_start":
			if data != nil {
				toolName, _ := data["toolName"].(string)
				if toolName != "" {
					summary := toolName
					if args, ok := data["arguments"].(map[string]any); ok {
						summary = copilotToolSummary(toolName, args)
					}
					buf.add(ActivityEvent{Type: "tool_use", Summary: summary, Timestamp: now})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
							"events": buf.snapshot(),
						})
					}
				}
			}

		case "tool.execution_partial_result":
			if data != nil {
				partial, _ := data["partialOutput"].(string)
				if partial != "" {
					// Show the last line of partial output as activity.
					lines := strings.Split(strings.TrimRight(partial, "\n"), "\n")
					lastLine := lines[len(lines)-1]
					buf.add(ActivityEvent{Type: "tool_use", Summary: truncStr(lastLine, 150), Timestamp: now})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
							"events": buf.snapshot(),
						})
					}
				}
			}

		case "tool.execution_complete":
			// Could log tool results, but we mainly care about tool starts for activity.

		case "result":
			r.vlog.Printf("copilot stream: result event: %s", truncStr(line, 500))

		case "assistant.reasoning":
			if data != nil {
				content, _ := data["content"].(string)
				if content != "" {
					buf.add(ActivityEvent{Type: "thinking", Summary: truncStr(content, 150), Timestamp: now})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.Send(envelope.EventChatActivity, sessionID, map[string]interface{}{
							"events": buf.snapshot(),
						})
					}
				}
			}

		case "assistant.message_delta", "assistant.turn_end",
			"session.mcp_server_status_changed", "session.mcp_servers_loaded",
			"session.tools_updated", "session.background_tasks_changed",
			"user.message":
			// Skip ephemeral/informational events.

		default:
			r.vlog.Printf("copilot stream: unhandled event type=%q", evType)
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmtAgentErrorFull(err, stderrBuf, resultText, lastRawEvent)
	}
	return &Result{Text: strings.TrimSpace(resultText)}, nil
}

// copilotToolSummary builds a short description of a copilot tool use.
func copilotToolSummary(name string, args map[string]any) string {
	// Try to extract a path argument (used by view, edit, create, etc.)
	if p, ok := args["path"].(string); ok && p != "" {
		return name + " " + p
	}
	switch name {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			desc, _ := args["description"].(string)
			if desc != "" {
				return desc
			}
			return "bash " + truncStr(cmd, 80)
		}
	case "grep":
		if p, ok := args["pattern"].(string); ok {
			return name + " " + truncStr(p, 60)
		}
	case "glob":
		if p, ok := args["pattern"].(string); ok {
			return name + " " + truncStr(p, 60)
		}
	case "report_intent":
		if intent, ok := args["intent"].(string); ok {
			return intent
		}
	}
	// Generic fallback: show first string argument value.
	for _, v := range args {
		if s, ok := v.(string); ok && s != "" {
			return name + " " + truncStr(s, 60)
		}
	}
	return name
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
	if params.Effort != "" {
		args = append(args, "--reasoning-effort", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, "-p", params.Prompt)
	return args
}

func buildCopilotArgs(params RunParams) []string {
	args := []string{
		"--output-format", "json",
		"--stream", "on",
		"--resume", params.SessionID,
		"-s",
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}
	args = append(args, "-p", params.Prompt)
	return args
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

// fmtAgentError formats an agent execution error, including the last few lines
// of stderr output when available for easier debugging.
func fmtAgentError(err error, stderrBuf *bytes.Buffer) error {
	stderr := strings.TrimSpace(stderrBuf.String())
	if stderr == "" {
		return fmt.Errorf("agent process failed: %w", err)
	}
	// Keep only the last few lines of stderr (most relevant).
	lines := strings.Split(stderr, "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return fmt.Errorf("agent process failed: %w\nstderr:\n%s", err, strings.Join(lines, "\n"))
}

// fmtAgentErrorFull formats an agent error with all available context:
// stderr, any result text parsed from the stream, and the last raw event.
func fmtAgentErrorFull(err error, stderrBuf *bytes.Buffer, resultText, lastRawEvent string) error {
	var parts []string
	parts = append(parts, fmt.Sprintf("agent process failed: %v", err))

	if resultText != "" {
		parts = append(parts, fmt.Sprintf("output: %s", truncStr(resultText, 500)))
	}

	stderr := strings.TrimSpace(stderrBuf.String())
	if stderr != "" {
		lines := strings.Split(stderr, "\n")
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		parts = append(parts, fmt.Sprintf("stderr:\n%s", strings.Join(lines, "\n")))
	}

	// If we have no other context, include the last raw event for diagnostics.
	if resultText == "" && stderr == "" && lastRawEvent != "" {
		parts = append(parts, fmt.Sprintf("last event: %s", truncStr(lastRawEvent, 500)))
	}

	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}
