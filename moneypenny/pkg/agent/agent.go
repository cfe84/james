package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	case "opencode":
		if runtime.GOOS == "windows" {
			appData := os.Getenv("APPDATA")
			if appData != "" {
				candidates = append(candidates,
					filepath.Join(appData, "npm", "opencode.cmd"),
					filepath.Join(appData, "npm", "opencode.ps1"),
				)
			}
			localAppData := os.Getenv("LOCALAPPDATA")
			if localAppData != "" {
				candidates = append(candidates,
					filepath.Join(localAppData, "Programs", "opencode", "opencode.exe"),
				)
			}
		} else {
			candidates = append(candidates, unixNodeBinCandidates(home, "opencode")...)
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

// withEnvironment replaces inherited variables with configured per-agent values.
func withEnvironment(base []string, additional map[string]string) []string {
	result := make([]string, 0, len(base)+len(additional))
	for _, item := range base {
		eq := strings.IndexByte(item, '=')
		if eq <= 0 {
			result = append(result, item)
			continue
		}
		name := strings.ToUpper(item[:eq])
		if strings.HasPrefix(name, "JAMES_HEM_") || strings.HasPrefix(name, "JAMES_GADGETS_") {
			continue
		}
		configured := false
		for name := range additional {
			if strings.EqualFold(name, item[:eq]) {
				configured = true
				break
			}
		}
		if !configured {
			result = append(result, item)
		}
	}
	for key, value := range additional {
		result = append(result, key+"="+value)
	}
	return result
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

// Result holds the parsed result from an agent invocation.
type Result struct {
	Text string // The text response extracted from the agent's JSON output
	// AgentSessionID is an agent-generated underlying session id. OpenCode
	// creates its own ses_... id rather than accepting a caller-selected id.
	AgentSessionID string
	// ContextTokens is the size of the underlying context after this turn
	// (input + cache tokens), when the agent reports it. 0 if unavailable
	// (e.g. Copilot, which exposes no cumulative context usage).
	ContextTokens int
	// ContextWindow is the model's max context, when the agent reports it
	// (Claude's modelUsage). 0 if unavailable.
	ContextWindow int
	// OpenCodeCost is the provider-reported cost for this invocation. OpenCode
	// reports it on each completed step; other agents leave it at zero.
	OpenCodeCost float64
	// OpenCodeRecovery records when a completed run had to be recovered from
	// the CLI's terminal event or its session export.
	OpenCodeRecovery string
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

const (
	maxActivitySummaryBytes = 512
	maxCapturedStderrBytes  = 1 << 20
	maxCopilotBufferedBytes = 32 << 20
	maxCopilotItemBytes     = 1 << 20
)

func newActivityBuffer(max int) *activityBuffer {
	return &activityBuffer{max: max, events: make([]ActivityEvent, 0, max)}
}

func (ab *activityBuffer) add(ev ActivityEvent) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	if len(ev.Summary) > maxActivitySummaryBytes {
		ev.Summary = truncStr(ev.Summary, maxActivitySummaryBytes-3)
	}
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

// boundedBuffer keeps only the tail of diagnostic output. Agent CLIs can write
// unbounded progress or tool output to stderr; retaining all of it in the
// long-lived Moneypenny process turns a noisy run into a memory leak.
type boundedBuffer struct {
	bytes.Buffer
	max int
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) >= b.max {
		b.Buffer.Reset()
		_, err := b.Buffer.Write(p[len(p)-b.max:])
		return len(p), err
	}
	_, err := b.Buffer.Write(p)
	if b.Len() > b.max {
		data := b.Bytes()
		kept := append([]byte(nil), data[len(data)-b.max:]...)
		b.Buffer.Reset()
		_, _ = b.Buffer.Write(kept)
	}
	return len(p), err
}

// RunParams contains parameters for running an agent.
type RunParams struct {
	SessionID    string
	Agent        string // "claude" for now
	Prompt       string
	SystemPrompt string // supplied on each invocation, including resumes
	Model        string // model override (e.g. "sonnet", "opus")
	Effort       string // reasoning effort level (e.g. "low", "medium", "high")
	// ContextTier selects copilot's context-window tier via --context (e.g.
	// "long_context" for the 1M window). Copilot-only; empty means the default
	// tier (no flag). Claude has no equivalent and ignores this.
	ContextTier string
	Yolo        bool
	Path        string            // working directory for the agent
	Environment map[string]string // additional environment variables for this agent run
	Resume      bool              // true for continue_session
	SessionDir  string            // per-session persistent dir (managed by handler)
	// AgentSessionID is the id passed to the underlying agent CLI. It is
	// decoupled from SessionID so custom compaction can substitute a fresh
	// underlying session. OpenCode generates this value after its first run.
	AgentSessionID    string
	openCodeDirectory string
	// NoPersistTurns suppresses persisting the run's thinking/intermediate-text
	// events as conversation turns. Used by distillation, which runs the agent
	// purely to maintain memory and must not pollute the live transcript.
	NoPersistTurns bool
	// Attachments holds absolute paths of files uploaded with this prompt.
	// Copilot receives them via repeated --attachment flags; Claude has no
	// attachment flag, so their containing directories are granted via
	// --add-dir and the paths are referenced in the prompt text.
	Attachments []string
	// ReplyChannelID, when non-zero, is the id of the channel (external
	// communication binding) whose reply should receive this run's final
	// assistant text. It is pure routing metadata: the runner ignores it, and
	// the handler enqueues the response to that channel's outbox on completion.
	ReplyChannelID int64
	// MarkReady requests that Hem surface this completed scheduled run in its
	// Ready group after the agent becomes idle.
	MarkReady bool
	// OperationID identifies the durable caller operation, when applicable.
	OperationID string
}

// agentSessionID returns the id to hand to the underlying agent CLI.
func (p RunParams) agentSessionID() string {
	if p.AgentSessionID != "" {
		return p.AgentSessionID
	}
	return p.SessionID
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
	env := withEnvironment(os.Environ(), params.Environment)
	env = PrependToPath(env, filepath.Dir(agentPath))
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
		// Some agents (notably claude) print fatal errors — e.g.
		// "API Error: 400 Not a valid API key for this workspace" — to
		// STDOUT rather than stderr, then exit non-zero. cmd.Output() still
		// captures that stdout in `out`, so fall back to it when stderr is
		// empty; otherwise the failure surfaces as an opaque "(stderr: )".
		detail := strings.TrimSpace(stderrBuf.String())
		if detail == "" {
			detail = strings.TrimSpace(string(out))
		}
		return "", fmt.Errorf("agent oneshot failed: %w (output: %s)", err, detail)
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

	// Build env: prepend the agent's directory to PATH so shebangs like
	// `#!/usr/bin/env node` find `node` next to `copilot`/`claude` (e.g. for
	// nvm-installed agents where the moneypenny service's PATH doesn't
	// otherwise include the node version's bin dir).
	agentDir := filepath.Dir(agentPath)
	env := withEnvironment(os.Environ(), params.Environment)
	env = PrependToPath(env, agentDir)
	env = append(env, "HEM_SESSION_ID="+params.SessionID)
	env = append(env, inv.env...)
	if params.Agent == "opencode" && params.Resume && params.AgentSessionID != "" {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		directory, lookupErr := openCodeSessionDirectory(lookupCtx, agentPath, params.AgentSessionID, params.Path, env)
		cancel()
		if errors.Is(lookupErr, errOpenCodeSessionMissing) {
			return nil, &OpenCodeFailure{Cause: lookupErr, Category: "session_not_found", ErrorName: "none", Resumed: true, LastEventType: "none"}
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("locating opencode session directory: %w", lookupErr)
		}
		params.openCodeDirectory = directory
		r.vlog.Printf("opencode resume: session=%s directory_differs=%t", params.SessionID, params.Path != "" && filepath.Clean(params.Path) != directory)
		inv = buildArgs(params)
	}
	cmd := exec.CommandContext(ctx, agentPath, inv.args...)
	if params.Path != "" {
		cmd.Dir = params.Path
	}
	cmd.Env = env
	if inv.stdin != "" {
		cmd.Stdin = strings.NewReader(inv.stdin)
	}

	stderrBuf := newBoundedBuffer(maxCapturedStderrBytes)
	if params.Agent == "opencode" {
		cmd.Stderr = stderrBuf
		r.vlog.Printf("exec: opencode session=%s resume=%t stdin_bytes=%d", params.SessionID, params.Resume && params.AgentSessionID != "", len(inv.stdin))
	} else {
		cmd.Stderr = io.MultiWriter(os.Stderr, stderrBuf)

		if inv.stdin != "" {
			r.vlog.Printf("exec: %s %s (prompt via stdin, %d bytes) extraEnv=%v", agentPath, strings.Join(inv.args, " "), len(inv.stdin), inv.env)
		} else {
			r.vlog.Printf("exec: %s %s extraEnv=%v", agentPath, strings.Join(inv.args, " "), inv.env)
		}
	}

	buf := newActivityBuffer(30)
	if !r.reserveProcess(params.SessionID, cmd, buf) {
		return nil, fmt.Errorf("agent session %s already has a running agent", params.SessionID)
	}

	defer func() {
		r.releaseProcess(params.SessionID, cmd)
	}()

	// All supported agents use streaming JSON output, each with its own schema.
	if params.Agent == "copilot" {
		return r.runCopilotStreaming(cmd, buf, params.SessionID, stderrBuf, !params.NoPersistTurns)
	}
	if params.Agent == "opencode" {
		return r.runOpenCodeStreaming(ctx, cmd, buf, params.SessionID, stderrBuf, !params.NoPersistTurns, params.Resume && params.AgentSessionID != "", params.AgentSessionID)
	}
	return r.runStreaming(cmd, buf, params.SessionID, stderrBuf, !params.NoPersistTurns)
}

func (r *Runner) reserveProcess(sessionID string, cmd *exec.Cmd, activity *activityBuffer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.procs[sessionID]; ok && existing != nil {
		return false
	}
	r.procs[sessionID] = cmd
	r.activity[sessionID] = activity
	return true
}

func (r *Runner) releaseProcess(sessionID string, owner *exec.Cmd) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.procs[sessionID]; ok && existing == owner {
		delete(r.procs, sessionID)
		delete(r.activity, sessionID)
	}
}

// OpenCodeFailure contains bounded, content-free diagnostics for daemon logs.
type OpenCodeFailure struct {
	Cause         error
	Category      string
	ErrorName     string
	StatusCode    int
	Events        int
	StderrBytes   int
	Resumed       bool
	HasSessionID  bool
	LastEventType string
}

func (e *OpenCodeFailure) Error() string {
	return fmt.Sprintf("opencode run failed: %v (category=%s error_name=%s status=%d events=%d stderr_bytes=%d resumed=%t session_id_seen=%t last_event=%s)",
		e.Cause, e.Category, e.ErrorName, e.StatusCode, e.Events, e.StderrBytes, e.Resumed, e.HasSessionID, e.LastEventType)
}

func (e *OpenCodeFailure) Unwrap() error { return e.Cause }

func openCodeErrorCategory(name, message string, status int) string {
	text := strings.ToLower(name + " " + message)
	switch {
	case strings.Contains(text, "session not found"), strings.Contains(text, "no session"),
		strings.Contains(text, "no conversation found"):
		return "session_not_found"
	case status == 401 || status == 403 || strings.Contains(text, "unauthorized") || strings.Contains(text, "invalid api key"):
		return "authentication"
	case status == 429 || strings.Contains(text, "rate limit"):
		return "rate_limit"
	case strings.Contains(text, "context length") || strings.Contains(text, "context window"):
		return "context_limit"
	case status >= 500 && status < 600:
		return "provider_unavailable"
	case strings.Contains(text, "timeout") || strings.Contains(text, "connection"):
		return "network"
	case strings.Contains(text, "model not found") || strings.Contains(text, "unknown model"):
		return "model_unavailable"
	default:
		return "unknown"
	}
}

// runOpenCodeStreaming parses OpenCode's --format json NDJSON stream. The
// session ID is generated by OpenCode and appears on every root-session event.
type openCodeWatchConfig struct {
	interval      time.Duration
	terminalGrace time.Duration
	exportAfter   time.Duration
	exportTimeout time.Duration
}

var defaultOpenCodeWatch = openCodeWatchConfig{interval: 5 * time.Second, terminalGrace: 5 * time.Second, exportAfter: 30 * time.Second, exportTimeout: 10 * time.Second}

func (r *Runner) runOpenCodeStreaming(ctx context.Context, cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf fmt.Stringer, persistTurns, resumed bool, knownAgentID string) (*Result, error) {
	return r.runOpenCodeStreamingWithWatch(ctx, cmd, buf, sessionID, stderrBuf, persistTurns, resumed, knownAgentID, defaultOpenCodeWatch)
}

func (r *Runner) runOpenCodeStreamingWithWatch(ctx context.Context, cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf fmt.Stringer, persistTurns, resumed bool, knownAgentID string, watch openCodeWatchConfig) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	started := time.Now()
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText, agentSessionID string
	var openCodeCost float64
	var streamErr error
	diagnostic := &OpenCodeFailure{Category: "unknown", ErrorName: "none", Resumed: resumed, LastEventType: "none"}
	var lastEvent, terminalAt atomic.Int64
	lastEvent.Store(started.UnixNano())
	var agentID atomic.Value
	agentID.Store(knownAgentID)
	type recoveredRun struct {
		text   string
		reason string
		cost   float64
	}
	recovered := make(chan recoveredRun, 1)
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		ticker := time.NewTicker(watch.interval)
		defer ticker.Stop()
		lastExport := started
		lastActivity := started
		for {
			select {
			case <-watchDone:
				return
			case <-ticker.C:
				now := time.Now()
				if ctx.Err() != nil {
					return
				}
				if terminal := terminalAt.Load(); terminal != 0 && now.Sub(time.Unix(0, terminal)) >= watch.terminalGrace {
					if err := cmd.Process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
						recovered <- recoveredRun{reason: "terminal_event"}
						_ = stdout.Close()
					}
					return
				}
				if now.Sub(time.Unix(0, lastEvent.Load())) >= watch.exportAfter && now.Sub(lastActivity) >= watch.exportAfter {
					buf.add(ActivityEvent{Type: "thinking", Summary: "OpenCode is still working; waiting for output", Timestamp: now.UTC().Format(time.RFC3339)})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{"events": buf.snapshot()})
					}
					lastActivity = now
				}
				id := agentID.Load().(string)
				if id == "" || now.Sub(time.Unix(0, lastEvent.Load())) < watch.exportAfter || now.Sub(lastExport) < watch.exportAfter {
					continue
				}
				lastExport = now
				if reply, complete := openCodeExportReply(ctx, cmd, id, started, watch.exportTimeout); complete {
					select {
					case <-watchDone:
						return
					default:
					}
					if err := cmd.Process.Kill(); err == nil || errors.Is(err, os.ErrProcessDone) {
						recovered <- recoveredRun{text: reply.text, cost: reply.cost, reason: "session_export"}
						_ = stdout.Close()
					}
					return
				}
			}
		}
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lastEvent.Store(time.Now().UnixNano())
		terminalAt.Store(0)
		diagnostic.Events++
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			r.vlog.Printf("opencode stream: unparseable line (%d bytes)", len(line))
			continue
		}
		if id, ok := event["sessionID"].(string); ok && id != "" {
			agentSessionID = id
			agentID.Store(id)
		}
		evType, _ := event["type"].(string)
		switch evType {
		case "text", "reasoning", "tool", "tool_use", "step_start", "step_finish", "error":
			diagnostic.LastEventType = evType
		default:
			diagnostic.LastEventType = "other"
		}
		part, _ := event["part"].(map[string]any)
		now := time.Now().UTC().Format(time.RFC3339)
		switch evType {
		case "step_start":
			buf.add(ActivityEvent{Type: "thinking", Summary: "OpenCode is working", Timestamp: now})
			if r.notifyWriter != nil {
				_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{"events": buf.snapshot()})
			}
		case "text":
			text, _ := part["text"].(string)
			if text != "" {
				resultText += text
				buf.add(ActivityEvent{Type: "text", Summary: text, Timestamp: now})
				if r.notifyWriter != nil {
					_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{"events": buf.snapshot()})
				}
			}
		case "reasoning":
			text, _ := part["text"].(string)
			if text != "" {
				buf.add(ActivityEvent{Type: "thinking", Summary: text, Timestamp: now})
				if persistTurns {
					r.emitPersistent(sessionID, "thinking", text)
				}
				if r.notifyWriter != nil {
					_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{"events": buf.snapshot()})
				}
			}
		case "tool", "tool_use":
			name, _ := part["tool"].(string)
			if name != "" {
				buf.add(ActivityEvent{Type: "tool_use", Summary: name, Timestamp: now})
				if r.notifyWriter != nil {
					_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{"events": buf.snapshot()})
				}
			}
		case "step_finish":
			if cost, ok := part["cost"].(float64); ok {
				openCodeCost += cost
			}
			if part["reason"] == "stop" {
				terminalAt.Store(time.Now().UnixNano())
			}
		case "error":
			streamErr = errors.New("opencode emitted an error event")
			if errorData, ok := event["error"].(map[string]any); ok {
				name, _ := errorData["name"].(string)
				switch name {
				case "APIError", "UnknownError", "AuthError", "ModelNotFoundError", "ProviderError":
					diagnostic.ErrorName = name
				default:
					diagnostic.ErrorName = "other"
				}
				detail, _ := errorData["data"].(map[string]any)
				message, _ := errorData["message"].(string)
				if message == "" {
					message, _ = detail["message"].(string)
				}
				status, _ := detail["statusCode"].(float64)
				if status >= 100 && status <= 599 {
					diagnostic.StatusCode = int(status)
				}
				diagnostic.Category = openCodeErrorCategory(diagnostic.ErrorName, message, diagnostic.StatusCode)
			}
		}
	}
	waitErr := cmd.Wait()
	close(watchDone)
	<-watchExited
	diagnostic.HasSessionID = agentSessionID != ""
	diagnostic.StderrBytes = len(stderrBuf.String())
	result := &Result{
		Text:           strings.TrimSpace(resultText),
		AgentSessionID: agentSessionID,
		OpenCodeCost:   openCodeCost,
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	scannerErr := scanner.Err()
	select {
	case recovery := <-recovered:
		if streamErr == nil && (scannerErr == nil || errors.Is(scannerErr, os.ErrClosed)) {
			if recovery.text != "" {
				result.Text = strings.TrimSpace(recovery.text)
			}
			if recovery.reason == "session_export" {
				result.OpenCodeCost = recovery.cost
			}
			result.OpenCodeRecovery = recovery.reason
			waitErr = nil
			scannerErr = nil
		}
	default:
	}
	if waitErr != nil {
		diagnostic.Cause = waitErr
		return result, diagnostic
	}
	if scannerErr != nil {
		diagnostic.Cause = fmt.Errorf("reading opencode stream: %w", scannerErr)
		return result, diagnostic
	}
	if streamErr != nil {
		diagnostic.Cause = streamErr
		return result, diagnostic
	}
	if result.Text == "" && ctx.Err() == nil {
		if reply, complete := openCodeExportReply(ctx, cmd, agentID.Load().(string), started, watch.exportTimeout); complete {
			result.Text = strings.TrimSpace(reply.text)
			result.OpenCodeCost = reply.cost
			result.OpenCodeRecovery = "session_export"
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
	}
	if result.Text == "" && persistTurns {
		diagnostic.Category = "no_reply"
		diagnostic.Cause = errors.New("opencode completed without a reply")
		return result, diagnostic
	}
	return result, nil
}

type openCodeExportResult struct {
	text string
	cost float64
}

func openCodeExportReply(ctx context.Context, cmd *exec.Cmd, sessionID string, started time.Time, timeout time.Duration) (openCodeExportResult, bool) {
	if sessionID == "" || ctx.Err() != nil {
		return openCodeExportResult{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	export := exec.CommandContext(ctx, cmd.Path, "export", sessionID)
	export.Dir, export.Env, export.Stderr = cmd.Dir, cmd.Env, io.Discard
	export.WaitDelay = 2 * time.Second
	stdout, err := export.StdoutPipe()
	if err != nil {
		return openCodeExportResult{}, false
	}
	if err := export.Start(); err != nil {
		return openCodeExportResult{}, false
	}
	stopClosing := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopClosing()
	const maxExportBytes = 8 << 20
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxExportBytes+1))
	if len(data) > maxExportBytes || readErr != nil {
		_ = export.Process.Kill()
		_ = export.Wait()
		return openCodeExportResult{}, false
	}
	if export.Wait() != nil || ctx.Err() != nil {
		return openCodeExportResult{}, false
	}
	var transcript struct {
		Messages []struct {
			Info struct {
				ID       string  `json:"id"`
				ParentID string  `json:"parentID"`
				Role     string  `json:"role"`
				Finish   string  `json:"finish"`
				Cost     float64 `json:"cost"`
				Time     struct {
					Created   int64 `json:"created"`
					Completed int64 `json:"completed"`
				} `json:"time"`
			} `json:"info"`
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Time struct {
					End int64 `json:"end"`
				} `json:"time"`
			} `json:"parts"`
		} `json:"messages"`
	}
	if json.Unmarshal(data, &transcript) != nil {
		return openCodeExportResult{}, false
	}
	var currentUser string
	for _, message := range transcript.Messages {
		if message.Info.Role == "user" && message.Info.Time.Created >= started.UnixMilli() {
			currentUser = message.Info.ID
		}
	}
	if currentUser == "" {
		return openCodeExportResult{}, false
	}
	var cost float64
	for _, message := range transcript.Messages {
		if message.Info.Role == "assistant" && message.Info.ParentID == currentUser && message.Info.Time.Completed != 0 {
			cost += message.Info.Cost
		}
	}
	for i := len(transcript.Messages) - 1; i >= 0; i-- {
		message := transcript.Messages[i]
		if message.Info.Role != "assistant" || message.Info.ParentID != currentUser {
			continue
		}
		if message.Info.Time.Completed == 0 || message.Info.Finish != "stop" {
			return openCodeExportResult{}, false
		}
		var text strings.Builder
		for _, part := range message.Parts {
			if part.Type == "text" && part.Time.End != 0 {
				text.WriteString(part.Text)
			}
		}
		if strings.TrimSpace(text.String()) == "" {
			return openCodeExportResult{}, false
		}
		return openCodeExportResult{text: text.String(), cost: cost}, true
	}
	return openCodeExportResult{}, false
}

// runStreaming runs a Claude agent with stream-json, parsing events into the activity buffer.
func (r *Runner) runStreaming(cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf fmt.Stringer, persistTurns bool) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText string
	var ctxTokens, ctxWindow int
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
						if persistTurns {
							r.emitPersistent(sessionID, "thinking", thinking)
						}
					}
				case "tool_use":
					buf.add(ActivityEvent{Type: "tool_use", Summary: toolSummary(b), Timestamp: now})
				case "text":
					text, _ := b["text"].(string)
					if text != "" {
						buf.add(ActivityEvent{Type: "text", Summary: text, Timestamp: now})
						if persistTurns {
							r.emitPersistent(sessionID, "text", text)
						}
					}
				}
			}
			// Send activity notification after processing assistant event
			if r.notifyWriter != nil && len(contentBlocks) > 0 {
				snapshot := buf.snapshot()
				_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
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
			// Claude reports token usage and per-model context windows in the
			// result event. The current context size is the prompt the model
			// just processed: input + cache-read + cache-creation tokens.
			ctxTokens, ctxWindow = parseClaudeUsage(event)
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
	return &Result{Text: resultText, ContextTokens: ctxTokens, ContextWindow: ctxWindow}, nil
}

// parseClaudeUsage extracts the current context size and the largest reported
// model context window from a Claude stream-json "result" event. Returns
// (0, 0) when the fields are absent.
func parseClaudeUsage(event map[string]any) (tokens, window int) {
	if usage, ok := event["usage"].(map[string]any); ok {
		num := func(k string) int {
			if v, ok := usage[k].(float64); ok {
				return int(v)
			}
			return 0
		}
		tokens = num("input_tokens") + num("cache_read_input_tokens") + num("cache_creation_input_tokens")
	}
	// modelUsage maps model name -> {contextWindow, ...}. Use the largest
	// window seen (the main model, vs. small helper models like haiku).
	if mu, ok := event["modelUsage"].(map[string]any); ok {
		for _, v := range mu {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if cw, ok := m["contextWindow"].(float64); ok && int(cw) > window {
				window = int(cw)
			}
		}
	}
	return tokens, window
}

// runCopilotStreaming runs a Copilot agent with --output-format json --stream on,
// parsing JSONL events into the activity buffer (same pattern as Claude streaming).
func (r *Runner) runCopilotStreaming(cmd *exec.Cmd, buf *activityBuffer, sessionID string, stderrBuf fmt.Stringer, persistTurns bool) (*Result, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent: %w", err)
	}

	var resultText string
	var lastRawEvent string
	var receivedResult bool
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
	var potBytes int
	appendPot := func(item potItem) {
		if len(item.content) > maxCopilotItemBytes {
			item.content = truncStr(item.content, maxCopilotItemBytes-3)
		}
		pot = append(pot, item)
		potBytes += len(item.content)
		for potBytes > maxCopilotBufferedBytes && len(pot) > 1 {
			potBytes -= len(pot[0].content)
			pot = pot[1:]
		}
	}
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
				_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
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
					appendPot(potItem{kind: "message", content: content, hasTools: hasTools, phase: phase})
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
					_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
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
						_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
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
					buf.add(ActivityEvent{Type: "tool_use", Summary: copilotToolOutputSummary(lastLine), Timestamp: now})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
							"events": buf.snapshot(),
						})
					}
				}
			}

		case "tool.execution_complete":
			// Could log tool results, but we mainly care about tool starts for activity.

		case "result":
			r.vlog.Printf("copilot stream: result event: %s", truncStr(line, 500))
			receivedResult = true

		case "assistant.reasoning":
			if data != nil {
				content, _ := data["content"].(string)
				if content != "" {
					appendPot(potItem{kind: "thinking", content: content})
					buf.add(ActivityEvent{Type: "thinking", Summary: content, Timestamp: now})
					if r.notifyWriter != nil {
						_ = r.notifyWriter.SendAsync(envelope.EventChatActivity, sessionID, map[string]interface{}{
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
			if persistTurns {
				r.emitPersistent(sessionID, "thinking", it.content)
			}
		case "message":
			// Skip empty tool-only messages (they exist only as boundaries).
			if it.content != "" && persistTurns {
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
		// Copilot can report a complete final answer, followed by a non-zero
		// process exit that it also exposes in its terminal result event. The
		// completed stream is authoritative for a conversational run: retaining
		// its answer prevents a successful agent response from being replaced by
		// a misleading generic execution failure. An incomplete stream, empty
		// answer, or scanner failure remains an error.
		if receivedResult && strings.TrimSpace(resultText) != "" && scanErr == nil {
			r.vlog.Printf("copilot stream completed with reply despite process exit: %v", err)
			return &Result{Text: strings.TrimSpace(resultText)}, nil
		}
		return nil, fmtAgentErrorFull(err, stderrBuf, resultText, lastRawEvent)
	}
	if scanErr != nil {
		return nil, fmtAgentErrorFull(fmt.Errorf("reading copilot stream: %w", scanErr), stderrBuf, resultText, lastRawEvent)
	}
	return &Result{Text: strings.TrimSpace(resultText)}, nil
}

// copilotToolOutputSummary reduces standard James gadget envelopes to a useful
// activity label. Other JSON and ordinary command output remain visible because
// they may be the intentional result of a tool invocation.
func copilotToolOutputSummary(output string) string {
	const limit = 150
	output = strings.TrimSpace(output)
	var envelope struct {
		Success *bool          `json:"success"`
		Error   string         `json:"error"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil || envelope.Success == nil {
		return truncStr(output, limit)
	}
	if !*envelope.Success {
		if envelope.Error != "" {
			return truncStr("Tool failed: "+envelope.Error, limit)
		}
		return "Tool failed"
	}
	if envelope.Data != nil {
		if path, ok := envelope.Data["path"].(string); ok && path != "" {
			return truncStr("Read memory: "+path, limit)
		}
		for _, key := range []string{"agents", "sessions", "items", "results"} {
			if items, ok := envelope.Data[key].([]any); ok {
				return fmt.Sprintf("Listed %d %s", len(items), key)
			}
		}
	}
	return "Tool completed successfully"
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
	env     []string // extra env vars to merge into cmd.Env
	cleanup func()   // optional cleanup, invoked after cmd.Wait()
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
	case "opencode":
		return buildOpenCodeArgs(params)
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
	case "opencode":
		return buildOpenCodeOneShotArgs(params)
	default:
		return buildClaudeOneShotArgs(params)
	}
}

func buildClaudeOneShotArgs(params RunParams) agentInvocation {
	args := []string{"--output-format", "text"}
	args = append(args, gadgetAccessArgs("claude", params)...)
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
	args = append(args, gadgetAccessArgs("copilot", params)...)
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}

	if params.Effort != "" {
		args = append(args, "--effort", params.Effort)
	}
	if params.ContextTier != "" {
		args = append(args, "--context", params.ContextTier)
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

func buildOpenCodeOneShotArgs(params RunParams) agentInvocation {
	args := []string{"run"}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--variant", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--auto")
	}
	args = append(args, withOpenCodeSystemPrompt(params))
	return agentInvocation{args: args}
}

func buildOpenCodeArgs(params RunParams) agentInvocation {
	args := []string{"run", "--format", "json", "--thinking"}
	if params.Resume && params.AgentSessionID != "" {
		args = append(args, "--session", params.AgentSessionID)
		if params.openCodeDirectory != "" {
			args = append(args, "--dir", params.openCodeDirectory)
		}
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--variant", params.Effort)
	}
	if params.Yolo {
		args = append(args, "--auto")
	}
	for _, path := range params.Attachments {
		args = append(args, "--file", path)
	}
	args = append(args, withOpenCodeSystemPrompt(params))
	return agentInvocation{args: args}
}

func withOpenCodeSystemPrompt(params RunParams) string {
	if params.SystemPrompt == "" {
		return params.Prompt
	}
	return "Instructions for this task:\n" + params.SystemPrompt + "\n\n---\n\n" + params.Prompt
}

func buildClaudeArgs(params RunParams) agentInvocation {
	var args []string
	if params.Resume {
		args = []string{
			"--output-format", "stream-json",
			"--verbose", // required for stream-json
			"--resume", params.agentSessionID(),
		}
	} else {
		args = []string{
			"--output-format", "stream-json",
			"--verbose",
			"--session-id", params.agentSessionID(),
		}
	}
	args = append(args, gadgetAccessArgs("claude", params)...)
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
	// Claude has no attachment flag; grant read access to the directories
	// containing uploaded attachments so it can open the absolute paths listed
	// in the prompt addendum.
	for _, dir := range attachmentDirs(params.Attachments) {
		args = append(args, "--add-dir", dir)
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

// attachmentDirs returns the unique parent directories of the given attachment
// paths, preserving first-seen order. Used to grant Claude read access via
// --add-dir (it has no native attachment flag).
func attachmentDirs(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(paths))
	var dirs []string
	for _, p := range paths {
		d := filepath.Dir(p)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	return dirs
}

// buildCopilotArgs constructs the command-line invocation for copilot.
func buildCopilotArgs(params RunParams) agentInvocation {
	args := []string{
		"--output-format", "json",
		"--stream", "on",
		"-s",
	}
	args = append(args, gadgetAccessArgs("copilot", params)...)
	// Copilot uses --session-id to CREATE a new session and --resume to
	// reattach to an existing one. Using --resume on a non-existent session
	// errors out with "No session, task, or name matched ...".
	if params.Resume {
		args = append(args, "--resume", params.agentSessionID())
	} else {
		args = append(args, "--session-id", params.agentSessionID())
	}
	if params.Model != "" {
		args = append(args, "--model", params.Model)
	}
	if params.Effort != "" {
		args = append(args, "--effort", params.Effort)
	}
	if params.ContextTier != "" {
		args = append(args, "--context", params.ContextTier)
	}
	if params.Yolo {
		args = append(args, "--yolo")
	}
	// Copilot ingests attachments natively via a repeatable --attachment flag
	// (images and documents). Only valid in prompt mode, which is how we invoke
	// it.
	for _, p := range params.Attachments {
		args = append(args, "--attachment", p)
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
func fmtAgentError(err error, stderrBuf fmt.Stringer) error {
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
func fmtAgentErrorFull(err error, stderrBuf fmt.Stringer, resultText, lastRawEvent string) error {
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
