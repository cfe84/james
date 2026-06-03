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
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"james/moneypenny/pkg/envelope"
)

// FindAgent locates an agent binary by name. It first checks PATH via
// exec.LookPath, then falls back to well-known installation directories
// (e.g. ~/.claude-cli on macOS/Linux, AppData on Windows).
func FindAgent(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		return "", fmt.Errorf("agent binary %q not found in PATH", name)
	}

	var candidates []string

	switch name {
	case "claude":
		if runtime.GOOS == "windows" {
			// npm-installed (most common): %APPDATA%\npm\claude.cmd
			appData := os.Getenv("APPDATA")
			if appData != "" {
				candidates = append(candidates,
					filepath.Join(appData, "npm", "claude.cmd"),
					filepath.Join(appData, "npm", "claude.ps1"),
					filepath.Join(appData, "Claude", "claude.exe"),
				)
			}
			// Standalone installers
			localAppData := os.Getenv("LOCALAPPDATA")
			if localAppData != "" {
				candidates = append(candidates,
					filepath.Join(localAppData, "AnthropicClaude", "claude.exe"),
					filepath.Join(localAppData, "Programs", "claude", "claude.exe"),
					filepath.Join(localAppData, "Programs", "moneypenny", "claude.exe"),
				)
			}
			// User-local (.claude-cli) installer
			candidates = append(candidates,
				filepath.Join(home, ".claude-cli", "CurrentVersion", "claude.exe"),
				filepath.Join(home, ".claude", "local", "claude.exe"),
			)
		} else {
			// macOS/Linux: standalone installer, npm global, Homebrew, version managers.
			candidates = append(candidates,
				filepath.Join(home, ".claude-cli", "CurrentVersion", "claude"),
				filepath.Join(home, ".claude", "local", "claude"),
			)
			candidates = append(candidates, unixNodeBinCandidates(home, "claude")...)
		}
	case "copilot":
		if runtime.GOOS == "windows" {
			appData := os.Getenv("APPDATA")
			if appData != "" {
				candidates = append(candidates,
					filepath.Join(appData, "npm", "copilot.cmd"),
					filepath.Join(appData, "npm", "copilot.ps1"),
				)
			}
			localAppData := os.Getenv("LOCALAPPDATA")
			if localAppData != "" {
				candidates = append(candidates, filepath.Join(localAppData, "Programs", "copilot", "copilot.exe"))
			}
		} else {
			candidates = append(candidates, unixNodeBinCandidates(home, "copilot")...)
		}
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", fmt.Errorf("agent binary %q not found in PATH or well-known locations", name)
}

// PrependToPath returns a copy of env with `dir` prepended to the PATH var.
// If PATH isn't set, it's added with just `dir`. Exported so handler code
// invoking agent binaries directly (e.g. for model listing) can apply the
// same fix.
func PrependToPath(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, e := range env {
		// Match PATH case-insensitively: Windows uses "Path", *nix uses "PATH".
		// We MUST preserve the original key casing — having both "Path=..."
		// and "PATH=..." in the same env block leads to undefined behavior on
		// Windows (CreateProcess receives duplicates, and one may shadow the
		// other depending on alphabetical ordering).
		if eq := strings.Index(e, "="); eq > 0 && strings.EqualFold(e[:eq], "PATH") {
			key := e[:eq]
			existing := e[eq+1:]
			if existing == "" {
				out = append(out, key+"="+dir)
			} else {
				out = append(out, key+"="+dir+string(os.PathListSeparator)+existing)
			}
			found = true
		} else {
			out = append(out, e)
		}
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}

// unixNodeBinCandidates returns common install paths for a Node-based CLI on
// macOS/Linux, covering Homebrew, npm global, nvm, Volta, Bun, and friends.
// nvm paths are globbed since they include a node version segment.
func unixNodeBinCandidates(home, bin string) []string {
	var c []string
	// System / Homebrew
	c = append(c,
		"/usr/local/bin/"+bin,
		"/opt/homebrew/bin/"+bin,
	)
	// User-local
	c = append(c,
		filepath.Join(home, ".local", "bin", bin),
		filepath.Join(home, "bin", bin),
		filepath.Join(home, ".npm-global", "bin", bin),
		filepath.Join(home, ".npm", "bin", bin),
		filepath.Join(home, ".yarn", "bin", bin),
		filepath.Join(home, ".bun", "bin", bin),
		filepath.Join(home, ".volta", "bin", bin),
	)
	// nvm: ~/.nvm/versions/node/<version>/bin/<bin> — glob to find any version.
	if matches, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", bin)); err == nil {
		c = append(c, matches...)
	}
	// fnm (fast node manager)
	if matches, err := filepath.Glob(filepath.Join(home, ".local", "share", "fnm", "node-versions", "*", "installation", "bin", bin)); err == nil {
		c = append(c, matches...)
	}
	return c
}

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
	SessionDir   string // per-session persistent dir (managed by handler)
}

// PersistentActivityFunc is called for activity events that should be persisted
// to the conversation (thinking, intermediate text). Tool use stays ephemeral.
type PersistentActivityFunc func(sessionID, eventType, content string)

// Runner manages agent subprocess execution.
type Runner struct {
	mu                   sync.Mutex
	procs                map[string]*exec.Cmd       // sessionID -> running process
	activity             map[string]*activityBuffer // sessionID -> recent activity
	vlog                 *log.Logger
	notifyWriter         *envelope.NotificationWriter
	onPersistentActivity PersistentActivityFunc
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

// SetPersistentActivityFunc sets the callback for persisting thinking/text events.
func (r *Runner) SetPersistentActivityFunc(f PersistentActivityFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onPersistentActivity = f
}

// emitPersistent calls the persistent activity callback if set.
func (r *Runner) emitPersistent(sessionID, eventType, content string) {
	r.mu.Lock()
	cb := r.onPersistentActivity
	r.mu.Unlock()
	if cb != nil {
		cb(sessionID, eventType, content)
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

// RunOneShot invokes an agent for a single prompt without any session
// management. No --session-id, no --resume, no streaming, no activity buffer,
// no persistent state. Returns the agent's final text response.
//
// Reusable for things like compacting a conversation summary or asking an
// agent a side question. The agent's `params.Path` (cwd) is honored so the
// agent has the same project context.
func (r *Runner) RunOneShot(ctx context.Context, params RunParams) (string, error) {
	agentPath, err := FindAgent(params.Agent)
	if err != nil {
		return "", err
	}

	inv := buildOneShotArgs(params)
	if inv.cleanup != nil {
		defer inv.cleanup()
	}

	cmd := exec.CommandContext(ctx, agentPath, inv.args...)
	if params.Path != "" {
		cmd.Dir = params.Path
	}
	env := PrependToPath(os.Environ(), filepath.Dir(agentPath))
	env = append(env, inv.env...)
	cmd.Env = env
	if inv.stdin != "" {
		cmd.Stdin = strings.NewReader(inv.stdin)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	r.vlog.Printf("oneshot exec: %s %s", agentPath, strings.Join(inv.args, " "))

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("agent oneshot failed: %w (stderr: %s)", err, strings.TrimSpace(stderrBuf.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Run invokes an agent with the given parameters. It blocks until the agent completes.
func (r *Runner) Run(ctx context.Context, params RunParams) (*Result, error) {
	agentPath, err := FindAgent(params.Agent)
	if err != nil {
		return nil, err
	}

	inv := buildArgs(params)
	if inv.cleanup != nil {
		defer inv.cleanup()
	}

	cmd := exec.CommandContext(ctx, agentPath, inv.args...)
	if params.Path != "" {
		cmd.Dir = params.Path
	}
	// Build env: prepend the agent's directory to PATH so shebangs like
	// `#!/usr/bin/env node` find `node` next to `copilot`/`claude` (e.g. for
	// nvm-installed agents where the moneypenny service's PATH doesn't
	// otherwise include the node version's bin dir).
	agentDir := filepath.Dir(agentPath)
	env := PrependToPath(os.Environ(), agentDir)
	env = append(env, "HEM_SESSION_ID="+params.SessionID)
	env = append(env, inv.env...)
	cmd.Env = env
	if inv.stdin != "" {
		cmd.Stdin = strings.NewReader(inv.stdin)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	if inv.stdin != "" {
		r.vlog.Printf("exec: %s %s (prompt via stdin, %d bytes) extraEnv=%v", agentPath, strings.Join(inv.args, " "), len(inv.stdin), inv.env)
	} else {
		r.vlog.Printf("exec: %s %s extraEnv=%v", agentPath, strings.Join(inv.args, " "), inv.env)
	}

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
						buf.add(ActivityEvent{Type: "thinking", Summary: thinking, Timestamp: now})
						r.emitPersistent(sessionID, "thinking", thinking)
					}
				case "tool_use":
					buf.add(ActivityEvent{Type: "tool_use", Summary: toolSummary(b), Timestamp: now})
				case "text":
					text, _ := b["text"].(string)
					if text != "" {
						buf.add(ActivityEvent{Type: "text", Summary: text, Timestamp: now})
						r.emitPersistent(sessionID, "text", text)
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
	// Copilot has no separate "result" event: the answer is conveyed purely
	// through assistant.message events. The model emits narration before each
	// tool call ("Now let me look at X") as its own assistant.message, then
	// emits its final answer as another assistant.message. Accumulating *every*
	// message into the reply made it very chatty (all the preambles leaked into
	// the bubble).
	//
	// Copilot labels each assistant.message with a "phase": "commentary" for
	// preamble narration and "final_answer" for the concluding reply (older
	// builds may omit it). We classify at end-of-stream (mirroring Claude's
	// split of train of thought vs final reply): the reply is the message(s)
	// tagged phase=="final_answer". When the provider supplies no phase labels,
	// we fall back to a positional heuristic: the trailing contiguous run of
	// no-tool messages (the model talking after it finished acting), with a
	// further fallback to the last non-empty message if there is no such run (so
	// a reply bundled with a housekeeping tool call is never lost). Everything
	// else — preamble narration and reasoning — is persisted as train of thought
	// (agent_text / thinking) in original order. Persistence is deferred to the
	// end because a message's role (preamble vs reply) isn't known until the
	// whole stream is seen.
	type potItem struct {
		kind     string // "thinking" or "message"
		content  string
		hasTools bool
		phase    string // copilot phase ("final_answer", "commentary", ...); "" if absent
	}
	var pot []potItem
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
				phase, _ := data["phase"].(string)
				toolReqs, hasToolReqs := data["toolRequests"].([]any)
				hasTools := hasToolReqs && len(toolReqs) > 0
				r.vlog.Printf("copilot stream: assistant.message content=%d bytes, toolRequests=%v, phase=%q",
					len(content), hasTools, phase)
				if content != "" || hasTools {
					// Record the message in the buffer even when content is
					// empty if it carries tools, so it still acts as a tool
					// boundary during the positional fallback classification (an
					// empty tool-only message must stop a trailing no-tool run).
					pot = append(pot, potItem{kind: "message", content: content, hasTools: hasTools, phase: phase})
				}
				if content != "" {
					buf.add(ActivityEvent{Type: "text", Summary: content, Timestamp: now})
				}
				// Parse tool requests for activity.
				if hasTools {
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
					pot = append(pot, potItem{kind: "thinking", content: content})
					buf.add(ActivityEvent{Type: "thinking", Summary: content, Timestamp: now})
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
	scanErr := scanner.Err()
	if scanErr != nil {
		r.vlog.Printf("copilot stream: scanner error: %v", scanErr)
	}

	// Classify the buffered events into reply vs train of thought.
	//
	// Preferred path: Copilot tags each assistant.message with a phase. When any
	// message carries a phase, trust it — the reply is exactly the message(s)
	// tagged "final_answer"; everything else is train of thought.
	usePhase := false
	for _, it := range pot {
		if it.kind == "message" && it.phase != "" {
			usePhase = true
			break
		}
	}

	isReply := make([]bool, len(pot))
	if usePhase {
		anyFinal := false
		for i, it := range pot {
			if it.kind == "message" && it.phase == "final_answer" && it.content != "" {
				isReply[i] = true
				anyFinal = true
			}
		}
		// Diagnostic for the (unexpected) mixed-stream shape: phases present but
		// no final_answer carried any text. We deliberately do NOT fall back to
		// the positional heuristic here (that would reintroduce commentary
		// leakage); an empty reply is correct when the turn ended on tool work.
		if !anyFinal {
			r.vlog.Printf("copilot stream: phase labels present but no final_answer content; reply will be empty")
		}
	} else {
		// Fallback (older Copilot builds without phase): the reply is the
		// trailing contiguous run of message items that carry no tool calls (the
		// model talking after it stopped acting). Walk backwards over message
		// items: the reply run starts at the first message item (scanning from
		// the end) that still has no tool calls, and stops as soon as we hit a
		// message item that DID carry a tool call.
		replyStart := len(pot)
		sawMessage := false
		lastMessageIdx := -1
		for i := len(pot) - 1; i >= 0; i-- {
			if pot[i].kind != "message" {
				continue
			}
			if lastMessageIdx < 0 && pot[i].content != "" {
				lastMessageIdx = i
			}
			if pot[i].hasTools {
				break
			}
			replyStart = i
			sawMessage = true
		}
		// Further fallback: no trailing no-tool message run, but the model did
		// produce text (e.g. its answer was bundled with a housekeeping tool
		// call). Use the last non-empty message as the reply so a real answer is
		// never hidden entirely in the train of thought.
		if !sawMessage && lastMessageIdx >= 0 {
			replyStart = lastMessageIdx
		}
		for i, it := range pot {
			if i >= replyStart && it.kind == "message" {
				isReply[i] = true
			}
		}
	}

	// Persist everything that isn't the reply as train of thought, in original
	// order. Reasoning -> "thinking"; preamble narration -> "text" (agent_text).
	// Reply messages are stored by the handler as the assistant turn, so we must
	// not also persist them here (that would duplicate them in the thread).
	var replyParts []string
	for i, it := range pot {
		if isReply[i] {
			replyParts = append(replyParts, it.content)
			continue
		}
		switch it.kind {
		case "thinking":
			r.emitPersistent(sessionID, "thinking", it.content)
		case "message":
			// Skip empty tool-only messages (they exist only as boundaries).
			if it.content != "" {
				r.emitPersistent(sessionID, "text", it.content)
			}
		}
	}

	// Join the reply parts. Trim each segment so provider newlines don't
	// compound, and separate with a blank line for clean Markdown rendering.
	// Computed before cmd.Wait so the error path still carries whatever partial
	// reply was produced.
	segments := make([]string, 0, len(replyParts))
	for _, t := range replyParts {
		if s := strings.TrimSpace(t); s != "" {
			segments = append(segments, s)
		}
	}
	resultText = strings.Join(segments, "\n\n")

	// Reap the process first so its exit error takes precedence, then surface
	// any stream read error (e.g. a line exceeding the scanner buffer) rather
	// than silently storing a truncated reply as a successful turn.
	if err := cmd.Wait(); err != nil {
		return nil, fmtAgentErrorFull(err, stderrBuf, resultText, lastRawEvent)
	}
	if scanErr != nil {
		return nil, fmtAgentErrorFull(fmt.Errorf("reading copilot stream: %w", scanErr), stderrBuf, resultText, lastRawEvent)
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

// agentInvocation describes how to run an agent: the command-line args plus
// optional stdin content (used for long prompts to avoid Windows' ~32KB
// command line length limit) plus optional extra env vars and a cleanup
// function (e.g. to remove a temp instructions dir).
type agentInvocation struct {
	args    []string
	stdin   string
	env     []string     // extra env vars to merge into cmd.Env
	cleanup func()       // optional cleanup, invoked after cmd.Wait()
}

// stdinPromptThreshold is the prompt length above which we route the prompt
// via stdin instead of as a -p positional argument. Chosen well below
// Windows' command-line limit (~32KB) to leave headroom for other args.
const stdinPromptThreshold = 4000

// buildArgs constructs the command-line invocation for the given agent.
func buildArgs(params RunParams) agentInvocation {
	switch params.Agent {
	case "copilot":
		return buildCopilotArgs(params)
	default:
		return buildClaudeArgs(params)
	}
}

// buildOneShotArgs constructs args for a single-shot invocation (no session
// state, plain-text output).
func buildOneShotArgs(params RunParams) agentInvocation {
	switch params.Agent {
	case "copilot":
		return buildCopilotOneShotArgs(params)
	default:
		return buildClaudeOneShotArgs(params)
	}
}

func buildClaudeOneShotArgs(params RunParams) agentInvocation {
	args := []string{"--output-format", "text"}
	if params.SystemPrompt != "" {
		args = append(args, "--system-prompt", params.SystemPrompt)
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--effort", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	inv := agentInvocation{}
	if needsStdin(params.Prompt) {
		args = append(args, "-p")
		inv.args = args
		inv.stdin = params.Prompt
		return inv
	}
	args = append(args, "-p", params.Prompt)
	inv.args = args
	return inv
}

func buildCopilotOneShotArgs(params RunParams) agentInvocation {
	args := []string{"--output-format", "text"}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--effort", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}
	inv := agentInvocation{}
	// Route the prompt via stdin rather than the `-p` flag. Copilot reads its
	// prompt from a non-TTY stdin when `-p` is omitted, and this is the only
	// reliable way to pass multi-line prompts: on Windows the npm-installed
	// `copilot.cmd` shim runs through cmd.exe, which truncates the command
	// line at the first newline, silently dropping everything after the first
	// line of an inline `-p` value. stdin sidesteps argv entirely (also avoids
	// the Windows ~32KB argv limit for long prompts). The `@file` form is not
	// usable — copilot treats `@` as an attachment, not prompt text.
	//
	// Guard the (handler-validated, so practically unreachable) empty-prompt
	// case explicitly: with neither `-p` nor stdin content, copilot could drop
	// into interactive mode and hang. Pass an explicit empty inline value.
	if params.Prompt == "" {
		args = append(args, "-p", "")
		inv.args = args
		return inv
	}
	inv.stdin = params.Prompt
	inv.args = args
	return inv
}

func buildClaudeArgs(params RunParams) agentInvocation {
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
		args = append(args, "--effort", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	// Route via stdin when:
	//   - the prompt is long (avoids Windows ~32KB cmdline limit), or
	//   - the prompt starts with "-" (else claude's CLI parser treats it as a flag).
	// claude reads the prompt from stdin when -p has no positional value.
	if needsStdin(params.Prompt) {
		args = append(args, "-p")
		return agentInvocation{args: args, stdin: params.Prompt}
	}
	args = append(args, "-p", params.Prompt)
	return agentInvocation{args: args}
}

// needsStdin returns true if the prompt should be routed via stdin instead of
// as a positional CLI argument.
func needsStdin(prompt string) bool {
	if len(prompt) > stdinPromptThreshold {
		return true
	}
	if strings.HasPrefix(prompt, "-") {
		return true
	}
	// Multi-line prompts must not be passed inline on Windows: npm installs
	// claude as a `.cmd`/`.ps1` shim that Go runs through cmd.exe, which
	// truncates the command line at the first newline (dropping everything
	// after the first line). Routing via stdin avoids argv entirely. It's
	// harmless to do this on every platform, so we don't branch on GOOS.
	if strings.ContainsAny(prompt, "\r\n") {
		return true
	}
	return false
}

func buildCopilotArgs(params RunParams) agentInvocation {
	args := []string{
		"--output-format", "json",
		"--stream", "on",
		"-s",
	}
	// Copilot uses --session-id to CREATE a new session and --resume to
	// reattach to an existing one. Using --resume on a non-existent session
	// errors out with "No session, task, or name matched ...".
	if params.Resume {
		args = append(args, "--resume", params.SessionID)
	} else {
		args = append(args, "--session-id", params.SessionID)
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--effort", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}

	inv := agentInvocation{}
	// Copilot has no --system-prompt flag. The supported mechanism is to place
	// an instructions file at .github/instructions/system.instructions.md inside
	// a directory pointed to by COPILOT_CUSTOM_INSTRUCTIONS_DIRS.
	// Write the system prompt to the session's persistent dir so it survives
	// resumes; no per-invocation cleanup needed (lifetime tied to the session).
	if params.SystemPrompt != "" && params.SessionDir != "" {
		instructionsDir := filepath.Join(params.SessionDir, "copilot-instructions")
		instructionsSubDir := filepath.Join(instructionsDir, ".github", "instructions")
		if err := os.MkdirAll(instructionsSubDir, 0700); err == nil {
			instructionsFile := filepath.Join(instructionsSubDir, "system.instructions.md")
			if err := os.WriteFile(instructionsFile, []byte(params.SystemPrompt), 0600); err == nil {
				inv.env = append(inv.env, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS="+instructionsDir)
			}
		}
	}

	// Route the prompt via stdin rather than the `-p` flag. Copilot reads its
	// prompt from a non-TTY stdin when `-p` is omitted. This is the only
	// reliable way to pass multi-line prompts: on Windows the npm-installed
	// `copilot.cmd` shim runs through cmd.exe, which truncates the command
	// line at the first newline, silently dropping everything after the first
	// line of an inline `-p` value. stdin sidesteps argv entirely (also avoids
	// the Windows ~32KB argv limit for long prompts). The `@file` form is not
	// usable — copilot treats `@` as an attachment, not prompt text.
	//
	// Guard the (handler-validated, so practically unreachable) empty-prompt
	// case explicitly: with neither `-p` nor stdin content, copilot could drop
	// into interactive mode and hang. Pass an explicit empty inline value.
	if params.Prompt == "" {
		args = append(args, "-p", "")
		inv.args = args
		return inv
	}
	inv.stdin = params.Prompt
	inv.args = args
	return inv
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
		parts = append(parts, fmt.Sprintf("output: %s", resultText))
	}

	stderr := strings.TrimSpace(stderrBuf.String())
	if stderr != "" {
		lines := strings.Split(stderr, "\n")
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		parts = append(parts, fmt.Sprintf("stderr:\n%s", strings.Join(lines, "\n")))
	}

	if lastRawEvent != "" {
		parts = append(parts, fmt.Sprintf("last event:\n%s", lastRawEvent))
	}

	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}
