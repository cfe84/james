package commands

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/hem/pkg/transport"
)

// Executor runs commands using the store and transport layer.
type Executor struct {
	store             *store.Store
	mi6KeyPath        string
	Version           string
	lastSessionStates map[string]string // sessionID → last known mpStatus ("working", "ready", etc.)
	clients           map[string]*transport.Client // cached per moneypenny name
	clientsMu         sync.Mutex
}

func New(s *store.Store, mi6KeyPath string) *Executor {
	return &Executor{
		store:             s,
		mi6KeyPath:        mi6KeyPath,
		lastSessionStates: make(map[string]string),
		clients:           make(map[string]*transport.Client),
	}
}

// CheckConnectivity pings all registered moneypennies and logs warnings for
// any that are unreachable. Intended to be called at server startup.
func (e *Executor) CheckConnectivity(logger *log.Logger) {
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		logger.Printf("WARNING: failed to list moneypennies: %v", err)
		return
	}
	if len(mps) == 0 {
		return
	}

	for _, mp := range mps {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := e.sendCommand(ctx, mp, "get_version", nil)
		cancel()
		if err != nil {
			logger.Printf("WARNING: moneypenny %q (%s) is unreachable: %v", mp.Name, moneypennyAddress(mp), err)
		} else {
			logger.Printf("moneypenny %q (%s) OK", mp.Name, moneypennyAddress(mp))
		}
	}
}

// SyncSessions queries all moneypennies for their sessions and tracks any
// that hem doesn't know about yet. This allows hem to adopt sessions that
// were created by other hem instances or before this hem was connected.
func (e *Executor) SyncSessions(logger *log.Logger) {
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		logger.Printf("sync: failed to list moneypennies: %v", err)
		return
	}

	for _, mp := range mps {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := e.sendCommand(ctx, mp, "list_sessions", nil)
		cancel()
		if err != nil {
			continue
		}

		var sessions []struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(resp.Data, &sessions); err != nil {
			continue
		}

		adopted := 0
		for _, s := range sessions {
			isNew, err := e.store.TrackSessionIfNew(s.SessionID, mp.Name)
			if err != nil {
				logger.Printf("sync: failed to track session %s: %v", s.SessionID, err)
			} else if isNew {
				adopted++
			}
		}
		if adopted > 0 {
			logger.Printf("sync: adopted %d new sessions from moneypenny %q", adopted, mp.Name)
		}
	}
}

// StartPeriodicSync runs SyncSessions on a regular interval in the background.
func (e *Executor) StartPeriodicSync(logger *log.Logger, interval time.Duration) {
	go func() {
		for {
			time.Sleep(interval)
			e.SyncSessions(logger)
		}
	}()
}

// CommandHelp maps verb+noun to help text.
var CommandHelp = map[string]string{
	"add moneypenny":      "Usage: hem add moneypenny -n NAME [--local | --fifo-folder DIR | --fifo-in PATH --fifo-out PATH | --mi6 ADDR]\n\nRegisters a new moneypenny instance.\n\nFlags:\n  -n, --name         Moneypenny name (required)\n  --local            Use default local FIFO path (~/.config/james/moneypenny/fifo)\n  --fifo-folder      Folder containing moneypenny-in and moneypenny-out FIFOs\n  --fifo-in          Path to moneypenny input FIFO\n  --fifo-out         Path to moneypenny output FIFO\n  --mi6              MI6 server address (host or host/session_id)\n  --session-id       MI6 session ID (combined with --mi6 host; uses default mi6 if --mi6 omitted)",
	"list moneypenny":     "Usage: hem list moneypennies\n\nLists all registered moneypennies with name, type, address, and default status.",
	"ping moneypenny":     "Usage: hem ping moneypenny -n NAME\n\nPings a moneypenny using get_version, displays version.\n\nFlags:\n  -n, --name         Moneypenny name (required)",
	"delete moneypenny":   "Usage: hem delete moneypenny -n NAME\n\nRemoves a registered moneypenny and its tracked sessions.\n\nFlags:\n  -n, --name         Moneypenny name (required)",
	"set-default moneypenny": "Usage: hem set-default moneypenny -n NAME\n\nSets the default moneypenny for session commands.\n\nFlags:\n  -n, --name         Moneypenny name (required)",
	"set-default agent":   "Usage: hem set-default agent VALUE\n\nSets the default agent for create session (fallback: claude).",
	"set-default path":    "Usage: hem set-default path VALUE\n\nSets the default working directory for create session (fallback: .).",
	"set-default mi6":     "Usage: hem set-default mi6 HOST\n\nSets the default MI6 server address (used by add moneypenny and test mi6 when --mi6 is omitted).",
	"get-default moneypenny": "Usage: hem get-default moneypenny\n\nShows the current default moneypenny.",
	"get-default agent":   "Usage: hem get-default agent\n\nShows the current default agent.",
	"get-default path":    "Usage: hem get-default path\n\nShows the current default path.",
	"get-default mi6":     "Usage: hem get-default mi6\n\nShows the current default MI6 server address.",
	"list default":        "Usage: hem list defaults\n\nShows all configured defaults.",
	"create session":      "Usage: hem create session [-m MONEYPENNY] PROMPT [flags]\n\nCreates a new session on a moneypenny and sends the initial prompt.\n\nFlags:\n  -m, --moneypenny   Moneypenny name (uses default if not set)\n  --agent            Agent to use (uses default, fallback: claude)\n  --name             Session name\n  --system-prompt    System prompt for the agent\n  --yolo             Skip permissions (--dangerously-skip-permissions)\n  --path             Working directory (uses default, fallback: .)\n  --async            Return immediately without waiting for response",
	"continue session":    "Usage: hem continue session SESSION_ID PROMPT [flags]\n\nSends a follow-up prompt to an existing session.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  --async            Return immediately without waiting for response",
	"stop session":        "Usage: hem stop session SESSION_ID\n\nStops a working session (kills the agent, session goes back to idle).\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"delete session":      "Usage: hem delete session SESSION_ID\n\nDeletes a session (kills agent if working, removes from moneypenny and local tracking).\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"state session":       "Usage: hem state session SESSION_ID\n\nShows the current state of a session (idle/working).\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"last session":        "Usage: hem last session SESSION_ID\n\nShows the last assistant response for a session.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"show session":        "Usage: hem show session SESSION_ID\n\nShows session parameters (agent, system_prompt, yolo, path, name, status).\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"update session":      "Usage: hem update session SESSION_ID [flags]\n\nUpdates session parameters.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  --name             Session name\n  --system-prompt    System prompt\n  --yolo             Yolo mode (true/false)\n  --path             Working directory",
	"history session":     "Usage: hem history session SESSION_ID [-n N] [--count C] [--from F]\n\nShows conversation history for a session.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  -n                 Number of turns to show (default: all)\n  --count            Page size (default: all; TUI uses 10)\n  --from             Offset from end for pagination",
	"list session":        "Usage: hem list sessions [-m MONEYPENNY]\n\nLists all sessions across all moneypennies.\n\nFlags:\n  -m, --moneypenny   Filter by moneypenny name",
	"test mi6":            "Usage: hem test mi6 --mi6 ADDRESS --session SESSION_ID\n\nTests connectivity to an MI6 server.\n\nFlags:\n  --mi6              MI6 server address (uses default if not set)\n  --session          Session ID to join (required)",
	"chat":                "Usage: hem chat [-m MONEYPENNY] [--session-id ID] [flags]\n\nInteractive chat with an agent. Creates a new session by default.\n\nFlags:\n  -m, --moneypenny   Moneypenny name (uses default if not set)\n  --session-id       Continue an existing session\n  --agent            Agent to use (uses default, fallback: claude)\n  --name             Session name\n  --system-prompt    System prompt for the agent\n  --yolo             Skip permissions\n  --path             Working directory",
	"create project":      "Usage: hem create project --name NAME [-m MONEYPENNY] [--path PATH] [--agent AGENT] [--system-prompt TEXT]\n\nCreates a new project.\n\nFlags:\n  --name             Project name (required)\n  -m, --moneypenny   Default moneypenny\n  --path             Working directory path\n  --agent            Default agent\n  --system-prompt    Default system prompt",
	"list project":        "Usage: hem list projects [--status STATUS]\n\nLists all projects.\n\nFlags:\n  --status           Filter by status (active, paused, done)",
	"show project":        "Usage: hem show project NAME_OR_ID\n\nShows project details.\n\nFlags:\n  --name             Project name (alternative to positional arg)",
	"update project":      "Usage: hem update project NAME_OR_ID [flags]\n\nUpdates project fields.\n\nFlags:\n  --name             New project name\n  --status           New status (active, paused, done)\n  -m, --moneypenny   Default moneypenny\n  --path             Working directory path\n  --agent            Default agent\n  --system-prompt    Default system prompt",
	"delete project":      "Usage: hem delete project NAME_OR_ID\n\nDeletes a project. Sessions keep their data but lose the project link.",
	"create template":     "Usage: hem create template --project PROJECT --name NAME [--agent AGENT] [--path PATH] [--system-prompt TEXT] [--prompt TEXT]\n\nCreates an agent template for a project.\n\nFlags:\n  --project          Project name or ID (required)\n  --name             Template name (required)\n  --agent            Agent to use (default: claude)\n  --path             Working directory\n  --system-prompt    System prompt\n  --prompt           Initial prompt",
	"list template":       "Usage: hem list templates --project PROJECT\n\nLists agent templates for a project.\n\nFlags:\n  --project          Project name or ID (required)",
	"delete template":     "Usage: hem delete template NAME_OR_ID --project PROJECT\n\nDeletes an agent template.\n\nFlags:\n  --project          Project name or ID (required for name lookup)",
	"use template":        "Usage: hem use template NAME_OR_ID --project PROJECT [--async]\n\nCreates a new session from a template.\n\nFlags:\n  --project          Project name or ID (required for name lookup)\n  --async            Return immediately without waiting",
	"complete session":    "Usage: hem complete session SESSION_ID\n\nMarks a session as completed in hem's local tracking.",
	"diff session":        "Usage: hem diff session SESSION_ID\n\nShows git diff for a session's working directory.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"run ":                "Usage: hem run [-m MONEYPENNY] [--path PATH] [--session-id ID] COMMAND\n\nExecutes a shell command on a remote moneypenny.\n\nFlags:\n  -m, --moneypenny   Moneypenny name (uses default if not set)\n  --path             Working directory on the remote host\n  --session-id       Use the moneypenny and path from this session",
	"commit session":      "Usage: hem commit session SESSION_ID -m MESSAGE\n\nStages all changes and commits in the session's working directory.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  -m                 Commit message (required)",
	"branch session":      "Usage: hem branch session SESSION_ID --name BRANCH_NAME\n\nCreates and switches to a new git branch in the session's working directory.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  --name             Branch name (required)",
	"push session":        "Usage: hem push session SESSION_ID\n\nPushes the current branch to origin with -u in the session's working directory.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)",
	"schedule session":    "Usage: hem schedule session SESSION_ID --at TIME --prompt PROMPT [--cron EXPR]\n\nSchedules a prompt for a session at a future time.\n\nFlags:\n  --session-id       Session ID (alternative to positional arg)\n  --at               When to send (RFC3339, or relative like +2h, +30m)\n  --prompt           Prompt to send\n  --cron             Cron expression for recurring schedules (e.g. '0 9 * * 1' for Mon 9am, '@every 2h', '@daily')",
	"list schedule":       "Usage: hem list schedules [--session-id SESSION_ID]\n\nLists scheduled prompts for a session.\n\nFlags:\n  --session-id       Session ID (required)",
	"cancel schedule":     "Usage: hem cancel schedule SCHEDULE_ID\n\nCancels a pending schedule.",
	"enable":              "Usage: hem enable SETTING\n\nEnables a boolean setting.\n\nAvailable settings:\n  schedule-system-prompt   Include schedule instructions in agent system prompts",
	"disable":             "Usage: hem disable SETTING\n\nDisables a boolean setting.\n\nAvailable settings:\n  schedule-system-prompt   Include schedule instructions in agent system prompts",
	"dashboard":           "Usage: hem dashboard [--project NAME] [--all]\n\nShows a dashboard of sessions grouped by attention state.\n\nFlags:\n  --project          Filter by project name\n  --all              Include completed sessions",
	"import session":       "Usage: hem import session FILE.jsonl|SESSION_ID [-m MONEYPENNY] [--name NAME] [--project PROJECT]\n\nImports an existing Claude Code session from a JSONL file or by session ID.\nIf the argument is not a file on disk, it is treated as a session ID and\nsearched for in ~/.claude/projects/ subdirectories.\n\nFlags:\n  -m, --moneypenny   Moneypenny name (uses default if not set)\n  --name             Session name (default: first user message)\n  --agent            Agent (default: claude)\n  --path             Working directory (default: from JSONL or default)\n  --project          Project name or ID",
}

// Dispatch routes a verb+noun+args to the appropriate handler.
func (e *Executor) Dispatch(verb, noun string, args []string) *protocol.Response {
	// Check for help flag in args.
	for _, a := range args {
		if a == "-h" || a == "--help" {
			key := verb + " " + noun
			if help, ok := CommandHelp[key]; ok {
				return protocol.OKResponse(TextResult{Message: help})
			}
			return protocol.ErrResponse(fmt.Sprintf("no help available for: %s %s", verb, noun))
		}
	}

	if verb == "dashboard" {
		return e.Dashboard(args)
	}

	if verb == "run" {
		return e.RunCommand(noun, args)
	}

	if verb == "get-version" {
		return protocol.OKResponse(map[string]string{"version": e.Version})
	}

	if verb == "enable" {
		return e.EnableSetting(noun)
	}
	if verb == "disable" {
		return e.DisableSetting(noun)
	}
	if verb == "list-directory" {
		return e.ListDirectory(noun, args)
	}

	switch verb + " " + noun {
	// Moneypenny commands
	case "add moneypenny":
		return e.AddMoneypenny(args)
	case "list moneypenny":
		return e.ListMoneypennies(args)
	case "ping moneypenny":
		return e.PingMoneypenny(args)
	case "delete moneypenny":
		return e.DeleteMoneypenny(args)
	case "set-default moneypenny":
		return e.SetDefaultMoneypenny(args)
	case "set-default agent":
		return e.SetDefaultValue("agent", args)
	case "set-default path":
		return e.SetDefaultValue("path", args)
	case "set-default mi6":
		return e.SetDefaultValue("mi6", args)
	case "get-default moneypenny", "get-default agent", "get-default path", "get-default mi6":
		return e.GetDefaultValue(noun)
	case "list default":
		return e.ListDefaults(args)

	// Session commands
	case "create session":
		return e.CreateSession(args)
	case "continue session":
		return e.ContinueSession(args)
	case "stop session":
		return e.StopSession(args)
	case "delete session":
		return e.DeleteSession(args)
	case "state session":
		return e.StateSession(args)
	case "last session":
		return e.LastSession(args)
	case "show session":
		return e.ShowSession(args)
	case "history session", "log session":
		return e.HistorySession(args)
	case "update session":
		return e.UpdateSession(args)
	case "list session":
		return e.ListSessions(args)
	case "test mi6":
		return e.TestMI6(args)

	// Project commands
	case "create project":
		return e.CreateProject(args)
	case "list project":
		return e.ListProjects(args)
	case "show project":
		return e.ShowProject(args)
	case "update project":
		return e.UpdateProject(args)
	case "delete project":
		return e.DeleteProject(args)

	// Template commands
	case "create template":
		return e.CreateTemplate(args)
	case "list template":
		return e.ListTemplates(args)
	case "delete template":
		return e.DeleteTemplate(args)
	case "use template":
		return e.UseTemplate(args)

	case "complete session":
		return e.CompleteSession(args)
	case "diff session":
		return e.DiffSession(args)
	case "commit session":
		return e.CommitSession(args)
	case "branch session":
		return e.BranchSession(args)
	case "push session":
		return e.PushSession(args)
	case "import session":
		return e.ImportSession(args)
	case "schedule session":
		return e.ScheduleSession(args)
	case "list schedule":
		return e.ListSchedules(args)
	case "cancel schedule":
		return e.CancelSchedule(args)
	default:
		return protocol.ErrResponse(fmt.Sprintf("unknown command: %s %s", verb, noun))
	}
}

// generateSessionID creates a UUID v4.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("failed to generate session ID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// clientForMoneypenny returns a (cached) transport.Client for the given moneypenny.
// Caching is important for FIFO clients: they hold a mutex that serialises
// concurrent writes to the same named pipe.
func (e *Executor) clientForMoneypenny(mp *store.Moneypenny) *transport.Client {
	e.clientsMu.Lock()
	defer e.clientsMu.Unlock()

	if c, ok := e.clients[mp.Name]; ok {
		return c
	}

	var c *transport.Client
	switch mp.TransportType {
	case store.TransportFIFO:
		c = transport.NewFIFOClient(mp.FIFOIn, mp.FIFOOut)
	case store.TransportMI6:
		c = transport.NewMI6Client(mp.MI6Addr, e.mi6KeyPath)
	default:
		return nil
	}
	e.clients[mp.Name] = c
	return c
}

// sendCommand sends a command to a moneypenny and returns the response.
func (e *Executor) sendCommand(ctx context.Context, mp *store.Moneypenny, method string, data interface{}) (*transport.Response, error) {
	client := e.clientForMoneypenny(mp)
	if client == nil {
		return nil, fmt.Errorf("unsupported transport type %q for moneypenny %q", mp.TransportType, mp.Name)
	}
	cmd := &transport.Command{
		Type:      "request",
		Method:    method,
		RequestID: generateSessionID(),
		Data:      data,
	}
	resp, err := client.Send(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("sending %s to %q: %w", method, mp.Name, err)
	}
	if resp.Status == "error" {
		return resp, fmt.Errorf("moneypenny %q returned error: %s (code: %s)", mp.Name, string(resp.Data), resp.ErrorCode)
	}
	return resp, nil
}

// pollUntilIdle polls a moneypenny session until it transitions from working to idle.
// Returns the last assistant response.
func (e *Executor) pollUntilIdle(ctx context.Context, mp *store.Moneypenny, sessionID string) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}

		resp, err := e.sendCommand(ctx, mp, "get_session", map[string]interface{}{"session_id": sessionID})
		if err != nil {
			return "", fmt.Errorf("polling session: %w", err)
		}

		var detail struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &detail); err != nil {
			return "", fmt.Errorf("parsing poll response: %w", err)
		}

		if detail.Status != "working" {
			// Fetch conversation to get the last assistant response.
			convResp, err := e.sendCommand(ctx, mp, "get_session_conversation", map[string]interface{}{"session_id": sessionID})
			if err != nil {
				return "", fmt.Errorf("fetching conversation: %w", err)
			}
			type turnInfo struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			var turns []turnInfo
			if len(convResp.Data) > 0 && convResp.Data[0] == '[' {
				if err := json.Unmarshal(convResp.Data, &turns); err != nil {
					return "", fmt.Errorf("parsing conversation: %w", err)
				}
			} else {
				var convData struct {
					Conversation []turnInfo `json:"conversation"`
				}
				if err := json.Unmarshal(convResp.Data, &convData); err != nil {
					return "", fmt.Errorf("parsing conversation: %w", err)
				}
				turns = convData.Conversation
			}
			for i := len(turns) - 1; i >= 0; i-- {
				if turns[i].Role == "assistant" {
					return turns[i].Content, nil
				}
			}
			return "", nil
		}
	}
}

// resolveSessionMoneypenny looks up which moneypenny a session belongs to.
// First checks local tracking, then scans all moneypennies as fallback.
func (e *Executor) resolveSessionMoneypenny(sessionID string) (*store.Moneypenny, error) {
	// Try local tracking first.
	mpName, err := e.store.GetSessionMoneypenny(sessionID)
	if err != nil {
		return nil, fmt.Errorf("looking up session %q: %w", sessionID, err)
	}
	if mpName != "" {
		mp, err := e.store.GetMoneypenny(mpName)
		if err != nil {
			return nil, fmt.Errorf("getting moneypenny %q: %w", mpName, err)
		}
		if mp != nil {
			return mp, nil
		}
	}

	// Fallback: scan all moneypennies for the session.
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, candidate := range mps {
		resp, err := e.sendCommand(ctx, candidate, "get_session", map[string]interface{}{"session_id": sessionID})
		if err == nil && resp != nil {
			// Track it locally for next time.
			e.store.TrackSession(sessionID, candidate.Name)
			return candidate, nil
		}
	}

	return nil, fmt.Errorf("session %q not found on any moneypenny", sessionID)
}

// parseFlagsFromArgs creates a new FlagSet, applies the setup function to register flags,
// parses the args, and returns remaining non-flag args.
// It reorders args so flags come before positional args, since Go's flag package
// stops parsing at the first non-flag argument.
func parseFlagsFromArgs(name string, args []string, setup func(fs *flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // suppress flag's built-in usage output on the server
	setup(fs)

	// Separate flags from positional args so flags can appear after positional args.
	// E.g., "SESSION_ID --yolo true" → "--yolo true SESSION_ID"
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			// Check if this flag takes a value (next arg is not a flag).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// Check if the flag is a boolean flag (no value needed).
				isBool := false
				fs.VisitAll(func(f *flag.Flag) {
					if f.Name == strings.TrimLeft(a, "-") {
						if _, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
							isBool = true
						}
					}
				})
				if !isBool {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			positional = append(positional, a)
		}
	}

	reordered := append(flagArgs, positional...)
	if err := fs.Parse(reordered); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

// formatTimestamp formats an ISO timestamp into a human-friendly format.
func formatTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("Jan 02 15:04")
}

// moneypennyAddress returns a display address for a moneypenny.
func moneypennyAddress(mp *store.Moneypenny) string {
	switch mp.TransportType {
	case store.TransportFIFO:
		dir := filepath.Dir(mp.FIFOIn)
		if filepath.Dir(mp.FIFOOut) == dir {
			return dir
		}
		return mp.FIFOIn + " / " + mp.FIFOOut
	case store.TransportMI6:
		return mp.MI6Addr
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Result types for structured responses
// ---------------------------------------------------------------------------

type TextResult struct {
	Message string `json:"message"`
}

type TableResult struct {
	Headers  []string   `json:"headers"`
	Rows     [][]string `json:"rows"`
	Warnings []string   `json:"warnings,omitempty"`
}

type SessionCreatedResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response,omitempty"`
	Async     bool   `json:"async"`
}

type SessionContinuedResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response,omitempty"`
	Async     bool   `json:"async"`
	Queued    bool   `json:"queued,omitempty"`
}

type SessionStateResult struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type SessionLastResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
}

type SessionShowResult struct {
	SessionID    string `json:"session_id"`
	Moneypenny   string `json:"moneypenny"`
	Name         string `json:"name"`
	Agent        string `json:"agent"`
	SystemPrompt string `json:"system_prompt"`
	Yolo         bool   `json:"yolo"`
	Path         string `json:"path"`
	Status       string `json:"status"`
}

type ConversationTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
}

type HistoryResult struct {
	SessionID    string             `json:"session_id"`
	Conversation []ConversationTurn `json:"conversation"`
	Total        int                `json:"total"` // total turns in the session
}

type ProjectResult struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	Moneypenny          string `json:"moneypenny"`
	Paths               string `json:"paths"`
	DefaultAgent        string `json:"default_agent"`
	DefaultSystemPrompt string `json:"default_system_prompt"`
}

// CommandResult is returned by the run command.
type CommandResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// ---------------------------------------------------------------------------
// Moneypenny commands
// ---------------------------------------------------------------------------

func (e *Executor) AddMoneypenny(args []string) *protocol.Response {
	var name, fifoFolder, fifoIn, fifoOut, mi6Addr, sessionID string
	var local bool

	remaining, err := parseFlagsFromArgs("add-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
		fs.BoolVar(&local, "local", false, "use default local FIFO path (~/.config/james/moneypenny/fifo)")
		fs.StringVar(&fifoFolder, "fifo-folder", "", "folder containing moneypenny-in and moneypenny-out FIFOs")
		fs.StringVar(&fifoIn, "fifo-in", "", "path to moneypenny input FIFO")
		fs.StringVar(&fifoOut, "fifo-out", "", "path to moneypenny output FIFO")
		fs.StringVar(&mi6Addr, "mi6", "", "MI6 address (host or host/session_id)")
		fs.StringVar(&sessionID, "session-id", "", "MI6 session ID (combined with --mi6 host)")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	_ = remaining

	// --local resolves to the default moneypenny FIFO path.
	if local && fifoFolder == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return protocol.ErrResponse(fmt.Sprintf("cannot determine home directory: %v", err))
		}
		fifoFolder = filepath.Join(home, ".config", "james", "moneypenny", "fifo")
	}

	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	// If --session-id is provided without --mi6, use default mi6.
	if sessionID != "" && mi6Addr == "" {
		if v, _ := e.store.GetDefault("mi6"); v != "" {
			mi6Addr = v
		}
	}

	// Combine mi6 host + session-id if both provided separately.
	if mi6Addr != "" && sessionID != "" {
		// If mi6Addr already contains a session (has /), replace it.
		if strings.Contains(mi6Addr, "/") {
			mi6Addr = mi6Addr[:strings.Index(mi6Addr, "/")] + "/" + sessionID
		} else {
			mi6Addr = mi6Addr + "/" + sessionID
		}
	}

	hasFIFOFolder := fifoFolder != ""
	hasFIFOPaths := fifoIn != "" || fifoOut != ""
	hasMI6 := mi6Addr != ""

	specCount := 0
	if hasFIFOFolder {
		specCount++
	}
	if hasFIFOPaths {
		specCount++
	}
	if hasMI6 {
		specCount++
	}
	if specCount != 1 {
		return protocol.ErrResponse("exactly one transport must be specified: --fifo-folder, --fifo-in/--fifo-out, or --mi6")
	}

	// MI6 address must contain a session ID (host/session_id).
	if hasMI6 && !strings.Contains(mi6Addr, "/") {
		return protocol.ErrResponse("MI6 address must include a session ID (host/session_id) — use --session-id or include it in --mi6")
	}

	mp := &store.Moneypenny{
		Name:      name,
		CreatedAt: time.Now(),
	}

	switch {
	case hasFIFOFolder:
		mp.TransportType = store.TransportFIFO
		mp.FIFOIn = filepath.Join(fifoFolder, "moneypenny-in")
		mp.FIFOOut = filepath.Join(fifoFolder, "moneypenny-out")
	case hasFIFOPaths:
		if fifoIn == "" || fifoOut == "" {
			return protocol.ErrResponse("both --fifo-in and --fifo-out are required when specifying FIFO paths directly")
		}
		mp.TransportType = store.TransportFIFO
		mp.FIFOIn = fifoIn
		mp.FIFOOut = fifoOut
	case hasMI6:
		mp.TransportType = store.TransportMI6
		mp.MI6Addr = mi6Addr
	}

	if err := e.store.AddMoneypenny(mp); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("storing moneypenny: %v", err))
	}

	// Validate connectivity by sending get_version.
	client := e.clientForMoneypenny(mp)
	pingCmd := &transport.Command{
		Type:      "request",
		Method:    "get_version",
		RequestID: "ping",
		Data:      nil,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Send(ctx, pingCmd)
	if err != nil {
		_ = e.store.DeleteMoneypenny(name)
		return protocol.ErrResponse(fmt.Sprintf("connectivity check failed for %q: %v", name, err))
	}
	if resp.Status == "error" {
		_ = e.store.DeleteMoneypenny(name)
		return protocol.ErrResponse(fmt.Sprintf("connectivity check failed for %q: moneypenny returned error: %s", name, string(resp.Data)))
	}

	var versionData struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp.Data, &versionData); err != nil {
		versionData.Version = string(resp.Data)
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Added moneypenny %q (%s). Version: %s", name, mp.TransportType, versionData.Version),
	})
}

func (e *Executor) ListMoneypennies(args []string) *protocol.Response {
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	result := TableResult{
		Headers: []string{"Name", "Type", "Address", "Default"},
	}
	for _, mp := range mps {
		def := ""
		if mp.IsDefault {
			def = "*"
		}
		result.Rows = append(result.Rows, []string{
			mp.Name,
			mp.TransportType,
			moneypennyAddress(mp),
			def,
		})
	}

	return protocol.OKResponse(result)
}

func (e *Executor) PingMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("ping-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	mp, err := e.store.GetMoneypenny(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", name))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := e.sendCommand(ctx, mp, "get_version", nil)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("ping failed: %v", err))
	}

	var versionData struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp.Data, &versionData); err != nil {
		versionData.Version = string(resp.Data)
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Moneypenny %q is reachable. Version: %s", name, versionData.Version),
	})
}

func (e *Executor) DeleteMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("delete-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	mp, err := e.store.GetMoneypenny(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", name))
	}

	if err := e.store.DeleteMoneypenny(name); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Deleted moneypenny %q (and its tracked sessions).", name),
	})
}

func (e *Executor) SetDefaultMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("set-default-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	if err := e.store.SetDefaultMoneypenny(name); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Default moneypenny set to %q.", name),
	})
}

// SetDefaultValue sets a default value (agent, path).
func (e *Executor) SetDefaultValue(key string, args []string) *protocol.Response {
	if len(args) == 0 {
		return protocol.ErrResponse(fmt.Sprintf("value is required: hem set-default %s VALUE", key))
	}
	value := args[0]

	if err := e.store.SetDefault(key, value); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Default %s set to %q.", key, value),
	})
}

// GetDefaultValue returns a default value.
func (e *Executor) GetDefaultValue(key string) *protocol.Response {
	value, err := e.store.GetDefault(key)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if value == "" {
		return protocol.OKResponse(TextResult{
			Message: fmt.Sprintf("No default %s set.", key),
		})
	}
	return protocol.OKResponse(TextResult{
		Message: value,
	})
}

// ListDefaults lists all defaults.
func (e *Executor) ListDefaults(args []string) *protocol.Response {
	defaults, err := e.store.ListDefaults()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Also include the moneypenny default from its own table.
	mp, err := e.store.GetDefaultMoneypenny()
	if err == nil && mp != nil {
		defaults["moneypenny"] = mp.Name
	}

	result := TableResult{
		Headers: []string{"Key", "Value"},
	}
	for k, v := range defaults {
		result.Rows = append(result.Rows, []string{k, v})
	}

	return protocol.OKResponse(result)
}

// validSettings lists the settings that can be toggled with enable/disable.
// validSettings lists the settings that can be toggled with enable/disable.
var validSettings = map[string]bool{
	"schedule-system-prompt":  true,
	"schedule-system-prompts": true,
}

// normalizeSetting maps aliases to canonical setting names.
func normalizeSetting(name string) string {
	if name == "schedule-system-prompts" {
		return "schedule-system-prompt"
	}
	return name
}

// EnableSetting enables a boolean setting.
func (e *Executor) EnableSetting(name string) *protocol.Response {
	if !validSettings[name] {
		return protocol.ErrResponse(fmt.Sprintf("unknown setting: %q", name))
	}
	name = normalizeSetting(name)
	if err := e.store.SetDefault(name, "true"); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return protocol.OKResponse(TextResult{Message: fmt.Sprintf("%s enabled.", name)})
}

// DisableSetting disables a boolean setting.
func (e *Executor) DisableSetting(name string) *protocol.Response {
	if !validSettings[name] {
		return protocol.ErrResponse(fmt.Sprintf("unknown setting: %q", name))
	}
	name = normalizeSetting(name)
	if err := e.store.SetDefault(name, "false"); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return protocol.OKResponse(TextResult{Message: fmt.Sprintf("%s disabled.", name)})
}

// ---------------------------------------------------------------------------
// Session commands
// ---------------------------------------------------------------------------

func (e *Executor) CreateSession(args []string) *protocol.Response {
	var mpName, sessionName, systemPrompt, pathArg, agentName, projectNameOrID string
	var yolo, async bool

	remaining, err := parseFlagsFromArgs("create-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&agentName, "agent", "", "agent to use")
		fs.StringVar(&sessionName, "name", "", "session name")
		fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt")
		fs.BoolVar(&yolo, "yolo", false, "enable yolo mode")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.BoolVar(&async, "async", false, "return immediately without waiting for response")
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Resolve project and apply its defaults when session-specific flags aren't provided.
	var projectID string
	if projectNameOrID != "" {
		proj, err := e.store.GetProject(projectNameOrID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
		}
		projectID = proj.ID
		if mpName == "" && proj.Moneypenny != "" {
			mpName = proj.Moneypenny
		}
		if agentName == "" && proj.DefaultAgent != "" {
			agentName = proj.DefaultAgent
		}
		if pathArg == "" && proj.Paths != "[]" && proj.Paths != "" {
			// Use the first path from the JSON array.
			var paths []string
			if json.Unmarshal([]byte(proj.Paths), &paths) == nil && len(paths) > 0 {
				pathArg = paths[0]
			}
		}
		if systemPrompt == "" && proj.DefaultSystemPrompt != "" {
			systemPrompt = proj.DefaultSystemPrompt
		}
	}

	// Apply stored defaults for agent and path when not specified.
	if agentName == "" {
		if v, _ := e.store.GetDefault("agent"); v != "" {
			agentName = v
		} else {
			agentName = "claude"
		}
	}
	if pathArg == "" {
		if v, _ := e.store.GetDefault("path"); v != "" {
			pathArg = v
		} else {
			pathArg = "."
		}
	}

	var mp *store.Moneypenny
	if mpName != "" {
		mp, err = e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
	} else {
		mp, err = e.store.GetDefaultMoneypenny()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse("no moneypenny specified and no default set")
		}
	}

	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		return protocol.ErrResponse("prompt is required (pass as trailing arguments)")
	}

	sessionID := generateSessionID()

	// Auto-generate a name from the prompt if none provided.
	if sessionName == "" {
		sessionName = prompt
		if len(sessionName) > 40 {
			sessionName = sessionName[:40]
		}
	}

	cmdData := map[string]interface{}{
		"agent":      agentName,
		"session_id": sessionID,
		"name":       sessionName,
		"prompt":     prompt,
		"path":       pathArg,
	}
	if systemPrompt != "" {
		cmdData["system_prompt"] = systemPrompt
	}
	if yolo {
		cmdData["yolo"] = true
	}

	if projectID != "" {
		if err := e.store.TrackSession(sessionID, mp.Name, projectID); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
		}
	} else {
		if err := e.store.TrackSession(sessionID, mp.Name); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
		}
	}

	ctx := context.Background()
	_, err = e.sendCommand(ctx, mp, "create_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if async {
		return protocol.OKResponse(SessionCreatedResult{
			SessionID: sessionID,
			Async:     true,
		})
	}

	// Poll until agent completes.
	response, err := e.pollUntilIdle(ctx, mp, sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Sync mode: the caller will see the response, mark as reviewed.
	_ = e.store.SetSessionReviewed(sessionID, true)

	return protocol.OKResponse(SessionCreatedResult{
		SessionID: sessionID,
		Response:  response,
	})
}

func (e *Executor) ContinueSession(args []string) *protocol.Response {
	var sessionID string
	var async bool

	remaining, err := parseFlagsFromArgs("continue-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.BoolVar(&async, "async", false, "return immediately without waiting for response")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
		remaining = remaining[1:]
	}

	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		return protocol.ErrResponse("prompt is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// If session was completed, reactivate it.
	if hemStatus, _ := e.store.GetSessionHemStatus(sessionID); hemStatus == "completed" {
		_ = e.store.SetSessionHemStatus(sessionID, "active")
	}

	// Mark as unreviewed — the response hasn't been seen yet.
	_ = e.store.SetSessionReviewed(sessionID, false)

	cmdData := map[string]interface{}{
		"session_id": sessionID,
		"prompt":     prompt,
	}

	ctx := context.Background()
	_, err = e.sendCommand(ctx, mp, "continue_session", cmdData)
	if err != nil {
		// If session is busy, queue the prompt instead.
		if isSessionNotIdle(err) {
			_, queueErr := e.sendCommand(ctx, mp, "queue_prompt", cmdData)
			if queueErr != nil {
				return protocol.ErrResponse(fmt.Sprintf("queueing prompt: %v", queueErr))
			}
			return protocol.OKResponse(SessionContinuedResult{
				SessionID: sessionID,
				Queued:    true,
			})
		}
		return protocol.ErrResponse(err.Error())
	}

	if async {
		return protocol.OKResponse(SessionContinuedResult{
			SessionID: sessionID,
			Async:     true,
		})
	}

	// Poll until agent completes.
	response, err := e.pollUntilIdle(ctx, mp, sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Sync mode: the caller will see the response, mark as reviewed.
	_ = e.store.SetSessionReviewed(sessionID, true)

	return protocol.OKResponse(SessionContinuedResult{
		SessionID: sessionID,
		Response:  response,
	})
}

// isSessionNotIdle checks if an error is a SESSION_NOT_IDLE error from moneypenny.
func isSessionNotIdle(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SESSION_NOT_IDLE")
}

func (e *Executor) StopSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("stop-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "stop_session", cmdData); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Session %s stopped.", sessionID),
	})
}

func (e *Executor) DeleteSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("delete-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	var warnings []string
	if _, err := e.sendCommand(ctx, mp, "delete_session", cmdData); err != nil {
		// Remote delete failed (moneypenny offline or session doesn't exist remotely).
		// Still clean up local tracking.
		warnings = append(warnings, fmt.Sprintf("remote delete failed: %v", err))
	}

	if err := e.store.DeleteTrackedSession(sessionID); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("removing local session tracking: %v", err))
	}

	msg := fmt.Sprintf("Session %s deleted.", sessionID)
	if len(warnings) > 0 {
		msg += " (warning: " + warnings[0] + ")"
	}
	return protocol.OKResponse(TextResult{
		Message: msg,
	})
}

func (e *Executor) StateSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("state-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var sessionData struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing session state: %v", err))
	}

	return protocol.OKResponse(SessionStateResult{
		SessionID: sessionID,
		Status:    sessionData.Status,
	})
}

func (e *Executor) LastSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("last-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session_conversation", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	type turnInfo struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var turns []turnInfo
	if len(resp.Data) > 0 && resp.Data[0] == '[' {
		if err := json.Unmarshal(resp.Data, &turns); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("parsing session data: %v", err))
		}
	} else {
		var sessionData struct {
			Conversation []turnInfo `json:"conversation"`
		}
		if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("parsing session data: %v", err))
		}
		turns = sessionData.Conversation
	}

	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "assistant" {
			return protocol.OKResponse(SessionLastResult{
				SessionID: sessionID,
				Response:  turns[i].Content,
			})
		}
	}

	return protocol.OKResponse(SessionLastResult{
		SessionID: sessionID,
		Response:  "(no assistant response found)",
	})
}

func (e *Executor) ShowSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("show-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing session data: %v", err))
	}

	result := SessionShowResult{
		SessionID:  sessionID,
		Moneypenny: mp.Name,
	}
	if v, ok := raw["name"].(string); ok {
		result.Name = v
	}
	if v, ok := raw["agent"].(string); ok {
		result.Agent = v
	}
	if v, ok := raw["system_prompt"].(string); ok {
		result.SystemPrompt = v
	}
	if v, ok := raw["yolo"].(bool); ok {
		result.Yolo = v
	}
	if v, ok := raw["path"].(string); ok {
		result.Path = v
	}
	if v, ok := raw["status"].(string); ok {
		result.Status = v
	}

	return protocol.OKResponse(result)
}

func (e *Executor) UpdateSession(args []string) *protocol.Response {
	var sessionID, name, systemPrompt, pathArg string
	var yoloStr, projectNameOrID string

	remaining, err := parseFlagsFromArgs("update-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&name, "name", "", "session name")
		fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt")
		fs.StringVar(&yoloStr, "yolo", "", "yolo mode (true/false)")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.StringVar(&projectNameOrID, "project", "", "move to project (name or ID)")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Build update data with only specified fields.
	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	hasUpdate := false
	if name != "" {
		cmdData["name"] = name
		hasUpdate = true
	}
	if systemPrompt != "" {
		cmdData["system_prompt"] = systemPrompt
		hasUpdate = true
	}
	if pathArg != "" {
		cmdData["path"] = pathArg
		hasUpdate = true
	}
	if yoloStr != "" {
		cmdData["yolo"] = yoloStr == "true"
		hasUpdate = true
	}

	// Handle project assignment (local to hem, not sent to moneypenny).
	if projectNameOrID != "" {
		proj, err := e.store.GetProject(projectNameOrID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
		}
		if err := e.store.SetSessionProject(sessionID, proj.ID); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		hasUpdate = true
	}

	if !hasUpdate {
		return protocol.ErrResponse("no fields to update (use --name, --system-prompt, --yolo, --path, --project)")
	}

	// Only send to moneypenny if there are moneypenny-level fields to update.
	if name != "" || systemPrompt != "" || pathArg != "" || yoloStr != "" {
		ctx := context.Background()
		if _, err := e.sendCommand(ctx, mp, "update_session", cmdData); err != nil {
			return protocol.ErrResponse(err.Error())
		}
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Session %s updated.", sessionID),
	})
}

func (e *Executor) HistorySession(args []string) *protocol.Response {
	var sessionID string
	var numTurns int
	var count int
	var from int

	remaining, err := parseFlagsFromArgs("history-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.IntVar(&numTurns, "n", 0, "number of turns to show (0 = all)")
		fs.IntVar(&count, "count", 0, "page size (0 = all)")
		fs.IntVar(&from, "from", 0, "offset from end")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	// If count is specified, use pagination; otherwise fetch all.
	if count > 0 {
		cmdData["count"] = count
		cmdData["from"] = from
	} else {
		cmdData["all"] = true
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session_conversation", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var sessionData struct {
		Conversation []ConversationTurn `json:"conversation"`
		Total        int                `json:"total"`
	}
	// Handle both new format (object with conversation+total) and old format (bare array).
	if len(resp.Data) > 0 && resp.Data[0] == '[' {
		var conv []ConversationTurn
		if err := json.Unmarshal(resp.Data, &conv); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("parsing conversation: %v", err))
		}
		sessionData.Conversation = conv
		sessionData.Total = len(conv)
	} else if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing conversation: %v", err))
	}

	conv := sessionData.Conversation
	if conv == nil {
		conv = []ConversationTurn{}
	}
	if numTurns > 0 && numTurns < len(conv) {
		conv = conv[len(conv)-numTurns:]
	}

	// Mark session as reviewed only if the last turn is from the assistant,
	// meaning the agent has finished and the user is seeing the final response.
	// If the agent is still working (last turn is user), don't mark reviewed yet.
	if len(conv) > 0 && conv[len(conv)-1].Role == "assistant" {
		_ = e.store.SetSessionReviewed(sessionID, true)
	}

	return protocol.OKResponse(HistoryResult{
		SessionID:    sessionID,
		Conversation: conv,
		Total:        sessionData.Total,
	})
}

func (e *Executor) ListSessions(args []string) *protocol.Response {
	var mpName, statusFilter string
	var showAll bool

	_, err := parseFlagsFromArgs("list-sessions", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name filter")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name filter")
		fs.BoolVar(&showAll, "all", false, "show all sessions including completed")
		fs.StringVar(&statusFilter, "status", "", "filter by hem_status (active, completed)")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var mps []*store.Moneypenny
	if mpName != "" {
		mp, err := e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
		mps = []*store.Moneypenny{mp}
	} else {
		mps, err = e.store.ListMoneypennies()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
	}

	// Build a set of tracked sessions with their hem_status for filtering.
	trackedSessions, _ := e.store.ListTrackedSessions("")
	hemStatusMap := make(map[string]string)
	for _, ts := range trackedSessions {
		hemStatusMap[ts.SessionID] = ts.HemStatus
	}

	result := TableResult{
		Headers: []string{"SessionID", "Name", "Status", "Moneypenny", "Created", "Last Active"},
	}

	ctx := context.Background()
	var warnings []string
	for _, mp := range mps {
		resp, err := e.sendCommand(ctx, mp, "list_sessions", nil)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("moneypenny %q is offline", mp.Name))
			continue
		}

		var sessions []struct {
			SessionID    string `json:"session_id"`
			Name         string `json:"name"`
			Status       string `json:"status"`
			CreatedAt    string `json:"created_at"`
			LastAccessed string `json:"last_accessed"`
		}
		if err := json.Unmarshal(resp.Data, &sessions); err != nil {
			continue
		}

		for _, s := range sessions {
			hemStatus := hemStatusMap[s.SessionID]
			if hemStatus == "" {
				hemStatus = "active"
			}

			// Apply hem_status filtering.
			if statusFilter != "" {
				if hemStatus != statusFilter {
					continue
				}
			} else if !showAll {
				// By default, hide completed sessions.
				if hemStatus == "completed" {
					continue
				}
			}

			created := formatTimestamp(s.CreatedAt)
			lastActive := formatTimestamp(s.LastAccessed)
			result.Rows = append(result.Rows, []string{s.SessionID, s.Name, s.Status, mp.Name, created, lastActive})
		}
	}

	if len(warnings) > 0 {
		result.Warnings = warnings
	}

	return protocol.OKResponse(result)
}

// ---------------------------------------------------------------------------
// Project commands
// ---------------------------------------------------------------------------

func (e *Executor) CreateProject(args []string) *protocol.Response {
	var name, mpName, pathArg, agentName, systemPrompt string

	_, err := parseFlagsFromArgs("create-project", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "project name")
		fs.StringVar(&mpName, "m", "", "default moneypenny")
		fs.StringVar(&mpName, "moneypenny", "", "default moneypenny")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.StringVar(&agentName, "agent", "", "default agent")
		fs.StringVar(&systemPrompt, "system-prompt", "", "default system prompt")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if name == "" {
		return protocol.ErrResponse("--name is required")
	}

	paths := "[]"
	if pathArg != "" {
		pathsJSON, _ := json.Marshal([]string{pathArg})
		paths = string(pathsJSON)
	}

	if agentName == "" {
		agentName = "claude"
	}

	now := time.Now()
	p := &store.Project{
		ID:                  generateSessionID(),
		Name:                name,
		Status:              "active",
		Moneypenny:          mpName,
		Paths:               paths,
		DefaultAgent:        agentName,
		DefaultSystemPrompt: systemPrompt,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	if err := e.store.CreateProject(p); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("creating project: %v", err))
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Created project %q (id: %s).", name, p.ID),
	})
}

func (e *Executor) ListProjects(args []string) *protocol.Response {
	var statusFilter string

	_, err := parseFlagsFromArgs("list-projects", args, func(fs *flag.FlagSet) {
		fs.StringVar(&statusFilter, "status", "", "filter by status")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	projects, err := e.store.ListProjects(statusFilter)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	result := TableResult{
		Headers: []string{"ID", "Name", "Status", "Moneypenny", "Agent", "Paths"},
	}
	for _, p := range projects {
		result.Rows = append(result.Rows, []string{
			p.ID, p.Name, p.Status, p.Moneypenny, p.DefaultAgent, p.Paths,
		})
	}

	return protocol.OKResponse(result)
}

func (e *Executor) ShowProject(args []string) *protocol.Response {
	var name string

	remaining, err := parseFlagsFromArgs("show-project", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "project name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if name == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("project name or ID is required")
		}
		name = remaining[0]
	}

	p, err := e.store.GetProject(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if p == nil {
		return protocol.ErrResponse(fmt.Sprintf("project %q not found", name))
	}

	return protocol.OKResponse(ProjectResult{
		ID:                  p.ID,
		Name:                p.Name,
		Status:              p.Status,
		Moneypenny:          p.Moneypenny,
		Paths:               p.Paths,
		DefaultAgent:        p.DefaultAgent,
		DefaultSystemPrompt: p.DefaultSystemPrompt,
	})
}

func (e *Executor) UpdateProject(args []string) *protocol.Response {
	var nameFlag, statusFlag, mpName, pathArg, agentName, systemPrompt string

	remaining, err := parseFlagsFromArgs("update-project", args, func(fs *flag.FlagSet) {
		fs.StringVar(&nameFlag, "name", "", "new project name")
		fs.StringVar(&statusFlag, "status", "", "new status (active, paused, done)")
		fs.StringVar(&mpName, "m", "", "default moneypenny")
		fs.StringVar(&mpName, "moneypenny", "", "default moneypenny")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.StringVar(&agentName, "agent", "", "default agent")
		fs.StringVar(&systemPrompt, "system-prompt", "", "default system prompt")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if len(remaining) == 0 {
		return protocol.ErrResponse("project name or ID is required as positional argument")
	}
	nameOrID := remaining[0]

	p, err := e.store.GetProject(nameOrID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if p == nil {
		return protocol.ErrResponse(fmt.Sprintf("project %q not found", nameOrID))
	}

	// Validate status if provided.
	if statusFlag != "" {
		switch statusFlag {
		case "active", "paused", "done":
			// valid
		default:
			return protocol.ErrResponse(fmt.Sprintf("invalid status %q: must be one of active, paused, done", statusFlag))
		}
	}

	var pName, pStatus, pMoneypenny, pPaths, pAgent, pSystemPrompt *string
	if nameFlag != "" {
		pName = &nameFlag
	}
	if statusFlag != "" {
		pStatus = &statusFlag
	}
	if mpName != "" {
		pMoneypenny = &mpName
	}
	if pathArg != "" {
		pathsJSON, _ := json.Marshal([]string{pathArg})
		pathsStr := string(pathsJSON)
		pPaths = &pathsStr
	}
	if agentName != "" {
		pAgent = &agentName
	}
	if systemPrompt != "" {
		pSystemPrompt = &systemPrompt
	}

	if pName == nil && pStatus == nil && pMoneypenny == nil && pPaths == nil && pAgent == nil && pSystemPrompt == nil {
		return protocol.ErrResponse("no fields to update")
	}

	if err := e.store.UpdateProject(p.ID, pName, pStatus, pMoneypenny, pPaths, pAgent, pSystemPrompt); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Project %q updated.", nameOrID),
	})
}

func (e *Executor) DeleteProject(args []string) *protocol.Response {
	nameOrID := ""
	if len(args) > 0 {
		nameOrID = args[0]
	}
	if nameOrID == "" {
		return protocol.ErrResponse("project name or ID is required")
	}

	p, err := e.store.GetProject(nameOrID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if p == nil {
		return protocol.ErrResponse(fmt.Sprintf("project %q not found", nameOrID))
	}

	if err := e.store.DeleteProject(p.ID); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Deleted project %q.", p.Name),
	})
}

// ---------------------------------------------------------------------------
// Template commands
// ---------------------------------------------------------------------------

func (e *Executor) CreateTemplate(args []string) *protocol.Response {
	var projectNameOrID, name, agent, pathArg, systemPrompt, prompt string
	var yolo bool

	_, err := parseFlagsFromArgs("create-template", args, func(fs *flag.FlagSet) {
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
		fs.StringVar(&name, "name", "", "template name")
		fs.StringVar(&agent, "agent", "claude", "agent")
		fs.StringVar(&pathArg, "path", "", "working directory")
		fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt")
		fs.StringVar(&prompt, "prompt", "", "initial prompt")
		fs.BoolVar(&yolo, "yolo", false, "enable yolo mode")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if projectNameOrID == "" {
		return protocol.ErrResponse("--project is required")
	}
	if name == "" {
		return protocol.ErrResponse("--name is required")
	}

	proj, err := e.store.GetProject(projectNameOrID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if proj == nil {
		return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
	}

	t := &store.AgentTemplate{
		ID:           generateSessionID(),
		ProjectID:    proj.ID,
		Name:         name,
		Agent:        agent,
		Path:         pathArg,
		SystemPrompt: systemPrompt,
		Prompt:       prompt,
		Yolo:         yolo,
		CreatedAt:    time.Now(),
	}

	if err := e.store.CreateTemplate(t); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Created template %q in project %q.", name, proj.Name),
	})
}

func (e *Executor) ListTemplates(args []string) *protocol.Response {
	var projectNameOrID string

	_, err := parseFlagsFromArgs("list-templates", args, func(fs *flag.FlagSet) {
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if projectNameOrID == "" {
		// List all templates across all projects.
		templates, projectNames, err := e.store.ListAllTemplates()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		result := TableResult{
			Headers: []string{"ID", "Name", "Project", "Agent", "Path", "Prompt", "Yolo"},
		}
		for _, t := range templates {
			prompt := t.Prompt
			if len(prompt) > 50 {
				prompt = prompt[:50] + "..."
			}
			yolo := "false"
			if t.Yolo {
				yolo = "true"
			}
			result.Rows = append(result.Rows, []string{t.ID, t.Name, projectNames[t.ID], t.Agent, t.Path, prompt, yolo})
		}
		return protocol.OKResponse(result)
	}

	proj, err := e.store.GetProject(projectNameOrID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if proj == nil {
		return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
	}

	templates, err := e.store.ListTemplates(proj.ID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	result := TableResult{
		Headers: []string{"ID", "Name", "Agent", "Path", "Prompt", "Yolo"},
	}
	for _, t := range templates {
		prompt := t.Prompt
		if len(prompt) > 50 {
			prompt = prompt[:50] + "..."
		}
		yolo := "false"
		if t.Yolo {
			yolo = "true"
		}
		result.Rows = append(result.Rows, []string{t.ID, t.Name, t.Agent, t.Path, prompt, yolo})
	}
	return protocol.OKResponse(result)
}

func (e *Executor) DeleteTemplate(args []string) *protocol.Response {
	var projectNameOrID string

	remaining, err := parseFlagsFromArgs("delete-template", args, func(fs *flag.FlagSet) {
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	nameOrID := ""
	if len(remaining) > 0 {
		nameOrID = remaining[0]
	}
	if nameOrID == "" {
		return protocol.ErrResponse("template name or ID is required")
	}

	// Resolve project for name lookup.
	var projectID string
	if projectNameOrID != "" {
		proj, err := e.store.GetProject(projectNameOrID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
		}
		projectID = proj.ID
	}

	t, err := e.store.GetTemplate(nameOrID, projectID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if t == nil {
		return protocol.ErrResponse(fmt.Sprintf("template %q not found", nameOrID))
	}

	if err := e.store.DeleteTemplate(t.ID); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Deleted template %q.", t.Name),
	})
}

// UseTemplate creates a session from a template.
func (e *Executor) UseTemplate(args []string) *protocol.Response {
	var projectNameOrID string
	var async bool

	remaining, err := parseFlagsFromArgs("use-template", args, func(fs *flag.FlagSet) {
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
		fs.BoolVar(&async, "async", true, "return immediately")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	nameOrID := ""
	if len(remaining) > 0 {
		nameOrID = remaining[0]
	}
	if nameOrID == "" {
		return protocol.ErrResponse("template name or ID is required")
	}

	// Resolve project.
	var projectID string
	var proj *store.Project
	if projectNameOrID != "" {
		proj, err = e.store.GetProject(projectNameOrID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
		}
		projectID = proj.ID
	}

	t, err := e.store.GetTemplate(nameOrID, projectID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if t == nil {
		return protocol.ErrResponse(fmt.Sprintf("template %q not found", nameOrID))
	}

	// Resolve project from template if not provided.
	if proj == nil {
		proj, err = e.store.GetProject(t.ProjectID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse("template's project not found")
		}
		projectID = proj.ID
	}

	// Resolve moneypenny from project.
	mpName := proj.Moneypenny
	agent := t.Agent
	if agent == "" {
		agent = proj.DefaultAgent
	}
	if agent == "" {
		agent = "claude"
	}
	pathArg := t.Path
	if pathArg == "" {
		var paths []string
		if json.Unmarshal([]byte(proj.Paths), &paths) == nil && len(paths) > 0 {
			pathArg = paths[0]
		}
	}
	if pathArg == "" {
		pathArg = "."
	}
	systemPrompt := t.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = proj.DefaultSystemPrompt
	}
	prompt := t.Prompt
	if prompt == "" {
		prompt = "Be ready"
	}

	var mp *store.Moneypenny
	if mpName != "" {
		mp, err = e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
	} else {
		mp, err = e.store.GetDefaultMoneypenny()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse("no moneypenny specified and no default set")
		}
	}

	sessionID := generateSessionID()
	sessionName := t.Name

	cmdData := map[string]interface{}{
		"agent":      agent,
		"session_id": sessionID,
		"name":       sessionName,
		"path":       pathArg,
	}
	if prompt != "" {
		cmdData["prompt"] = prompt
	}
	if systemPrompt != "" {
		cmdData["system_prompt"] = systemPrompt
	}
	if t.Yolo {
		cmdData["yolo"] = true
	}

	if err := e.store.TrackSession(sessionID, mp.Name, projectID); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
	}

	ctx := context.Background()
	_, err = e.sendCommand(ctx, mp, "create_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if async {
		return protocol.OKResponse(SessionCreatedResult{
			SessionID: sessionID,
			Async:     true,
		})
	}

	response, pollErr := e.pollUntilIdle(ctx, mp, sessionID)
	if pollErr != nil {
		return protocol.ErrResponse(pollErr.Error())
	}
	_ = e.store.SetSessionReviewed(sessionID, true)

	return protocol.OKResponse(SessionCreatedResult{
		SessionID: sessionID,
		Response:  response,
	})
}

func (e *Executor) CompleteSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("complete-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	current, err := e.store.GetSessionHemStatus(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	newStatus := "completed"
	if current == "completed" {
		newStatus = "active"
	}

	if err := e.store.SetSessionHemStatus(sessionID, newStatus); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Session %s marked as %s.", sessionID, newStatus),
	})
}

func (e *Executor) DiffSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("diff-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	// Find which moneypenny has this session.
	mpName, err := e.store.GetSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mpName == "" {
		return protocol.ErrResponse(fmt.Sprintf("session %q not tracked", sessionID))
	}

	mp, err := e.store.GetMoneypenny(mpName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "git_diff", map[string]interface{}{
		"session_id": sessionID,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result struct {
		Diff string `json:"diff"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing diff: %v", err))
	}

	return protocol.OKResponse(TextResult{Message: result.Diff})
}

// RunCommand executes a shell command on a remote moneypenny.
// The noun parameter captures the first word after "run" which may be part of the command.
func (e *Executor) RunCommand(noun string, args []string) *protocol.Response {
	var mpName, pathArg, sessionID string

	remaining, err := parseFlagsFromArgs("run", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&pathArg, "path", "", "working directory")
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Build the command string from noun + remaining args.
	var parts []string
	if noun != "" {
		parts = append(parts, noun)
	}
	parts = append(parts, remaining...)
	command := strings.Join(parts, " ")
	if command == "" {
		return protocol.ErrResponse("command is required")
	}

	// If session-id is provided, resolve moneypenny and path from it.
	if sessionID != "" {
		mp, err := e.resolveSessionMoneypenny(sessionID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mpName == "" {
			mpName = mp.Name
		}
		if pathArg == "" {
			// Get session details to find path.
			ctx := context.Background()
			resp, err := e.sendCommand(ctx, mp, "get_session", map[string]interface{}{"session_id": sessionID})
			if err != nil {
				return protocol.ErrResponse(err.Error())
			}
			var detail struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(resp.Data, &detail); err == nil && detail.Path != "" {
				pathArg = detail.Path
			}
		}
	}

	// Resolve moneypenny.
	if mpName == "" {
		mpName, _ = e.store.GetDefault("moneypenny")
	}
	if mpName == "" {
		return protocol.ErrResponse("moneypenny is required (use -m or set a default)")
	}

	mp, err := e.store.GetMoneypenny(mpName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "execute_command", map[string]interface{}{
		"command": command,
		"path":    pathArg,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result CommandResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(result)
}

// ListDirectoryResult is returned by ListDirectory.
type ListDirectoryResult struct {
	Path    string          `json:"path"`
	Entries []DirEntryInfo  `json:"entries"`
}

type DirEntryInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

func (e *Executor) ListDirectory(noun string, args []string) *protocol.Response {
	var mpName, pathArg string

	remaining, err := parseFlagsFromArgs("list-directory", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&pathArg, "path", "", "directory path")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Path can come from noun or --path flag or remaining args.
	if pathArg == "" && noun != "" {
		pathArg = noun
	}
	if pathArg == "" && len(remaining) > 0 {
		pathArg = remaining[0]
	}
	if pathArg == "" {
		pathArg = "/"
	}

	if mpName == "" {
		mpName, _ = e.store.GetDefault("moneypenny")
	}
	if mpName == "" {
		return protocol.ErrResponse("moneypenny is required (use -m or set a default)")
	}

	mp, err := e.store.GetMoneypenny(mpName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "list_directory", map[string]interface{}{
		"path": pathArg,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result ListDirectoryResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(result)
}

func (e *Executor) ImportSession(args []string) *protocol.Response {
	var mpName, sessionName, agentName, pathArg, projectNameOrID string

	remaining, err := parseFlagsFromArgs("import-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&sessionName, "name", "", "session name")
		fs.StringVar(&agentName, "agent", "", "agent (default: claude)")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.StringVar(&projectNameOrID, "project", "", "project name or ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if len(remaining) == 0 {
		return protocol.ErrResponse("JSONL file path or session ID is required")
	}

	jsonlPath := remaining[0]
	if _, err := os.Stat(jsonlPath); err != nil {
		// Not a file on disk — treat as a session ID and search ~/.claude/projects/
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return protocol.ErrResponse(fmt.Sprintf("getting home directory: %v", err))
		}
		projectsDir := filepath.Join(homeDir, ".claude", "projects")
		targetName := remaining[0] + ".jsonl"
		found := ""
		_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if !info.IsDir() && info.Name() == targetName {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found == "" {
			return protocol.ErrResponse(fmt.Sprintf("could not find Claude session file for session ID: %s", remaining[0]))
		}
		jsonlPath = found
	}

	// Read and parse the JSONL file.
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("reading file: %v", err))
	}

	var sessionID, cwd string
	var conversation []ConversationTurn

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		var msgType string
		if raw, ok := entry["type"]; ok {
			json.Unmarshal(raw, &msgType)
		}

		// Extract session ID and cwd from the first user message.
		if sessionID == "" {
			if raw, ok := entry["sessionId"]; ok {
				json.Unmarshal(raw, &sessionID)
			}
		}
		if cwd == "" {
			if raw, ok := entry["cwd"]; ok {
				json.Unmarshal(raw, &cwd)
			}
		}

		switch msgType {
		case "user":
			var msg struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			}
			if raw, ok := entry["message"]; ok {
				if json.Unmarshal(raw, &msg) == nil && msg.Role == "user" {
					if text, ok := msg.Content.(string); ok {
						conversation = append(conversation, ConversationTurn{
							Role:    "user",
							Content: text,
						})
					}
				}
			}
		case "assistant":
			var msg struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			}
			if raw, ok := entry["message"]; ok {
				if json.Unmarshal(raw, &msg) == nil && msg.Role == "assistant" {
					// Content is an array of blocks; extract text blocks.
					if blocks, ok := msg.Content.([]interface{}); ok {
						var texts []string
						for _, block := range blocks {
							if bm, ok := block.(map[string]interface{}); ok {
								if bm["type"] == "text" {
									if text, ok := bm["text"].(string); ok {
										texts = append(texts, text)
									}
								}
							}
						}
						if len(texts) > 0 {
							conversation = append(conversation, ConversationTurn{
								Role:    "assistant",
								Content: strings.Join(texts, "\n"),
							})
						}
					}
				}
			}
		}
	}

	if sessionID == "" {
		return protocol.ErrResponse("could not extract session ID from JSONL file")
	}
	if len(conversation) == 0 {
		return protocol.ErrResponse("no conversation found in JSONL file")
	}

	// Apply defaults.
	if agentName == "" {
		if v, _ := e.store.GetDefault("agent"); v != "" {
			agentName = v
		} else {
			agentName = "claude"
		}
	}
	if pathArg == "" {
		if cwd != "" {
			pathArg = cwd
		} else if v, _ := e.store.GetDefault("path"); v != "" {
			pathArg = v
		} else {
			pathArg = "."
		}
	}
	if sessionName == "" {
		// Use first user message as name.
		for _, t := range conversation {
			if t.Role == "user" {
				sessionName = t.Content
				if len(sessionName) > 40 {
					sessionName = sessionName[:40]
				}
				break
			}
		}
	}

	// Resolve moneypenny.
	var mp *store.Moneypenny
	if mpName != "" {
		mp, err = e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
	} else {
		mp, err = e.store.GetDefaultMoneypenny()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse("no moneypenny specified and no default set")
		}
	}

	// Build conversation turns for moneypenny.
	var turns []map[string]string
	for _, t := range conversation {
		turns = append(turns, map[string]string{
			"role":    t.Role,
			"content": t.Content,
		})
	}

	cmdData := map[string]interface{}{
		"session_id":   sessionID,
		"name":         sessionName,
		"agent":        agentName,
		"path":         pathArg,
		"conversation": turns,
	}

	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "import_session", cmdData); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Track session locally.
	var projectID string
	if projectNameOrID != "" {
		proj, err := e.store.GetProject(projectNameOrID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectNameOrID))
		}
		projectID = proj.ID
	}

	if projectID != "" {
		if err := e.store.TrackSession(sessionID, mp.Name, projectID); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
		}
	} else {
		if err := e.store.TrackSession(sessionID, mp.Name); err != nil {
			return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
		}
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Imported session %s (%d turns) from %s", sessionID, len(conversation), filepath.Base(jsonlPath)),
	})
}

func (e *Executor) Dashboard(args []string) *protocol.Response {
	var projectFilter string
	var showAll bool

	_, err := parseFlagsFromArgs("dashboard", args, func(fs *flag.FlagSet) {
		fs.StringVar(&projectFilter, "project", "", "filter by project name")
		fs.BoolVar(&showAll, "all", false, "include completed sessions")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Get all tracked sessions.
	trackedSessions, err := e.store.ListTrackedSessions("")
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// If filtering by project, resolve the project and filter sessions.
	var projectIDFilter string
	if projectFilter != "" {
		proj, err := e.store.GetProject(projectFilter)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if proj == nil {
			return protocol.ErrResponse(fmt.Sprintf("project %q not found", projectFilter))
		}
		projectIDFilter = proj.ID
	}

	// Build project name cache.
	projects, _ := e.store.ListProjects("")
	projectNames := make(map[string]string)
	for _, p := range projects {
		projectNames[p.ID] = p.Name
	}

	type dashboardEntry struct {
		SessionID  string
		Name       string
		Project    string
		MPStatus   string // moneypenny status (idle/working)
		HemStatus  string // active/completed
		Moneypenny string
		LastActive string
		SortKey    int // 0=REVIEW, 1=WORKING, 2=COMPLETED
	}

	// Filter tracked sessions first.
	var filteredSessions []*store.Session
	for _, sess := range trackedSessions {
		if projectIDFilter != "" && sess.ProjectID != projectIDFilter {
			continue
		}
		if sess.HemStatus == "completed" && !showAll {
			continue
		}
		filteredSessions = append(filteredSessions, sess)
	}

	// Group filtered sessions by moneypenny name.
	sessionsByMP := make(map[string][]*store.Session)
	for _, sess := range filteredSessions {
		sessionsByMP[sess.MoneypennyName] = append(sessionsByMP[sess.MoneypennyName], sess)
	}

	// Query each moneypenny in parallel with list_sessions (one call per moneypenny).
	type mpSessionInfo struct {
		Status       string `json:"status"`
		Name         string `json:"name"`
		SessionID    string `json:"session_id"`
		LastAccessed string `json:"last_accessed"`
	}

	type mpResult struct {
		mpName   string
		sessions map[string]mpSessionInfo // keyed by session_id
		err      error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan mpResult, len(sessionsByMP))
	var wg sync.WaitGroup

	for mpName := range sessionsByMP {
		mp, err := e.store.GetMoneypenny(mpName)
		if err != nil || mp == nil {
			resultCh <- mpResult{mpName: mpName, err: fmt.Errorf("moneypenny not found")}
			continue
		}
		wg.Add(1)
		go func(mp *store.Moneypenny) {
			defer wg.Done()
			resp, err := e.sendCommand(ctx, mp, "list_sessions", nil)
			if err != nil {
				resultCh <- mpResult{mpName: mp.Name, err: err}
				return
			}
			var sessions []mpSessionInfo
			if err := json.Unmarshal(resp.Data, &sessions); err != nil {
				resultCh <- mpResult{mpName: mp.Name, err: err}
				return
			}
			m := make(map[string]mpSessionInfo, len(sessions))
			for _, s := range sessions {
				m[s.SessionID] = s
			}
			resultCh <- mpResult{mpName: mp.Name, sessions: m}
		}(mp)
	}

	// Close channel after all goroutines complete.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results into a map by moneypenny name.
	mpData := make(map[string]map[string]mpSessionInfo)
	for res := range resultCh {
		if res.err != nil {
			log.Printf("dashboard: moneypenny %q query failed: %v", res.mpName, res.err)
		} else {
			mpData[res.mpName] = res.sessions
		}
	}

	// Build dashboard entries.
	var entries []dashboardEntry

	for _, sess := range filteredSessions {
		var mpStatus, sessionName, lastAccessed string

		if mpSessions, ok := mpData[sess.MoneypennyName]; ok {
			if info, found := mpSessions[sess.SessionID]; found {
				mpStatus = info.Status
				sessionName = info.Name
				lastAccessed = info.LastAccessed
			} else {
				mpStatus = "unknown"
				log.Printf("dashboard: session %s not found on moneypenny %q (mp has %d sessions)",
					sess.SessionID, sess.MoneypennyName, len(mpSessions))
			}
		} else {
			mpStatus = "offline"
			log.Printf("dashboard: moneypenny %q unreachable for session %s", sess.MoneypennyName, sess.SessionID)
		}

		projectName := projectNames[sess.ProjectID]

		// Determine attention category.
		// 0=READY (idle, unreviewed), 1=WORKING, 2=IDLE (idle, reviewed), 3=COMPLETED
		sortKey := 1 // WORKING
		if sess.HemStatus == "completed" {
			sortKey = 3
		} else if mpStatus == "unknown" {
			// Moneypenny is reachable but doesn't know this session — always IDLE.
			sortKey = 2
		} else if mpStatus == "idle" || mpStatus == "offline" {
			if sess.Reviewed {
				sortKey = 2 // IDLE
			} else {
				sortKey = 0 // READY
			}
		}

		// Display "ready" instead of "idle" for unreviewed sessions.
		displayStatus := mpStatus
		if (mpStatus == "idle" || mpStatus == "offline") && !sess.Reviewed {
			displayStatus = "ready"
		}

		displayName := sessionName
		if displayName == "" {
			if len(sess.SessionID) > 12 {
				displayName = sess.SessionID[:12] + "..."
			} else {
				displayName = sess.SessionID
			}
		}

		lastActiveFormatted := formatTimestamp(lastAccessed)

		entries = append(entries, dashboardEntry{
			SessionID:  sess.SessionID,
			Name:       displayName,
			Project:    projectName,
			MPStatus:   displayStatus,
			HemStatus:  sess.HemStatus,
			Moneypenny: sess.MoneypennyName,
			LastActive: lastActiveFormatted,
			SortKey:    sortKey,
		})
	}

	// Sort by attention level (REVIEW first, then WORKING, then COMPLETED).
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].SortKey < entries[i].SortKey {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Update state cache (clients detect working→ready transitions).
	for _, entry := range entries {
		e.lastSessionStates[entry.SessionID] = entry.MPStatus
	}

	result := TableResult{
		Headers: []string{"SessionID", "Name", "Project", "Status", "Moneypenny", "Last Activity"},
	}
	for _, entry := range entries {
		result.Rows = append(result.Rows, []string{
			entry.SessionID, entry.Name, entry.Project, entry.MPStatus + " (" + entry.HemStatus + ")", entry.Moneypenny, entry.LastActive,
		})
	}

	return protocol.OKResponse(result)
}

// ---------------------------------------------------------------------------
// MI6 commands
// ---------------------------------------------------------------------------

func (e *Executor) TestMI6(args []string) *protocol.Response {
	var mi6Addr, sessionID string

	_, err := parseFlagsFromArgs("test-mi6", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mi6Addr, "mi6", "", "MI6 server address")
		fs.StringVar(&sessionID, "session", "", "session ID to join")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Fall back to default mi6.
	if mi6Addr == "" {
		if v, _ := e.store.GetDefault("mi6"); v != "" {
			mi6Addr = v
		}
	}

	if mi6Addr == "" {
		return protocol.ErrResponse("--mi6 is required (or set a default with 'hem set-default mi6 HOST')")
	}
	if sessionID == "" {
		return protocol.ErrResponse("--session is required")
	}

	addr := mi6Addr + "/" + sessionID

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := transport.TestMI6(ctx, addr, e.mi6KeyPath); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("MI6 connectivity test failed: %v", err))
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("MI6 connectivity OK. Connected to %s, session %s.", mi6Addr, sessionID),
	})
}

// ScheduleSession schedules a prompt for a session at a future time.
func (e *Executor) ScheduleSession(args []string) *protocol.Response {
	var sessionID, atStr, prompt, cronExpr string

	remaining, err := parseFlagsFromArgs("schedule-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&atStr, "at", "", "when to send (RFC3339 or relative like +2h)")
		fs.StringVar(&prompt, "prompt", "", "prompt to send")
		fs.StringVar(&cronExpr, "cron", "", "cron expression for recurring schedules")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
		remaining = remaining[1:]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}

	if atStr == "" {
		return protocol.ErrResponse("--at is required (e.g. --at +2h or --at 2026-03-07T15:00:00Z)")
	}

	if prompt == "" && len(remaining) > 0 {
		prompt = strings.TrimSpace(strings.Join(remaining, " "))
	}
	if prompt == "" {
		return protocol.ErrResponse("--prompt is required")
	}

	// Parse the time.
	scheduledAt, err := parseScheduleTime(atStr)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("invalid --at value: %v", err))
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id":   sessionID,
		"prompt":       prompt,
		"scheduled_at": scheduledAt.UTC().Format(time.RFC3339),
	}
	if cronExpr != "" {
		cmdData["cron_expr"] = cronExpr
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "schedule", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result struct {
		ScheduleID  int64  `json:"schedule_id"`
		SessionID   string `json:"session_id"`
		ScheduledAt string `json:"scheduled_at"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Scheduled prompt for session %s at %s (schedule #%d)", result.SessionID, result.ScheduledAt, result.ScheduleID),
	})
}

// ListSchedules lists scheduled prompts for a session.
func (e *Executor) ListSchedules(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("list-schedules", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "list_schedules", map[string]interface{}{
		"session_id": sessionID,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result ScheduleListResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	if len(result.Schedules) == 0 {
		return protocol.OKResponse(TextResult{Message: "No schedules found."})
	}

	var rows [][]string
	for _, s := range result.Schedules {
		truncPrompt := s.Prompt
		if len(truncPrompt) > 60 {
			truncPrompt = truncPrompt[:57] + "..."
		}
		rows = append(rows, []string{
			fmt.Sprintf("%d", s.ID),
			s.Status,
			s.ScheduledAt,
			truncPrompt,
		})
	}

	return protocol.OKResponse(TableResult{
		Headers: []string{"ID", "Status", "Scheduled At", "Prompt"},
		Rows:    rows,
	})
}

// CancelSchedule cancels a pending schedule.
func (e *Executor) CancelSchedule(args []string) *protocol.Response {
	if len(args) == 0 {
		return protocol.ErrResponse("schedule_id is required")
	}

	var scheduleID int64
	if _, err := fmt.Sscanf(args[0], "%d", &scheduleID); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("invalid schedule_id: %s", args[0]))
	}

	// We need a session to find the moneypenny. For cancel, we need the schedule ID
	// but we don't know which moneypenny has it. We'll need --session-id or try all.
	var sessionID string
	if len(args) > 1 {
		// Check for --session-id flag.
		for i, a := range args {
			if a == "--session-id" && i+1 < len(args) {
				sessionID = args[i+1]
			}
		}
	}

	if sessionID == "" {
		return protocol.ErrResponse("--session-id is required for cancel schedule")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	_, err = e.sendCommand(ctx, mp, "cancel_schedule", map[string]interface{}{
		"schedule_id": scheduleID,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Schedule #%d cancelled.", scheduleID),
	})
}

// ScheduleListResult holds the list_schedules response.
type ScheduleListResult struct {
	Schedules []ScheduleInfoResult `json:"schedules"`
}

// ScheduleInfoResult holds info about a single schedule.
type ScheduleInfoResult struct {
	ID          int64  `json:"id"`
	SessionID   string `json:"session_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

// parseScheduleTime parses a time string that can be RFC3339 or relative like "+2h", "+30m".
func parseScheduleTime(s string) (time.Time, error) {
	// Try RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try relative time.
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err == nil {
			return time.Now().UTC().Add(d), nil
		}
	}

	// Try common date/time formats (local time).
	formats := []string{
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"15:04",
	}
	for _, f := range formats {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			// For time-only, use today's date.
			if f == "15:04" {
				now := time.Now()
				t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
				if t.Before(now) {
					t = t.Add(24 * time.Hour)
				}
			}
			return t.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot parse time %q (use RFC3339, +2h, or YYYY-MM-DD HH:MM)", s)
}

// CommitSession stages all changes and commits in a session's working directory.
func (e *Executor) CommitSession(args []string) *protocol.Response {
	var sessionID, message string

	remaining, err := parseFlagsFromArgs("commit-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&message, "m", "", "commit message")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
		remaining = remaining[1:]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}
	if message == "" && len(remaining) > 0 {
		message = strings.Join(remaining, " ")
	}
	if message == "" {
		return protocol.ErrResponse("-m (commit message) is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "git_commit", map[string]interface{}{
		"session_id": sessionID,
		"message":    message,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(TextResult{Message: result.Output})
}

// BranchSession creates and switches to a new git branch in a session's working directory.
func (e *Executor) BranchSession(args []string) *protocol.Response {
	var sessionID, branchName string

	remaining, err := parseFlagsFromArgs("branch-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.StringVar(&branchName, "name", "", "branch name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
		remaining = remaining[1:]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}
	if branchName == "" && len(remaining) > 0 {
		branchName = remaining[0]
	}
	if branchName == "" {
		return protocol.ErrResponse("--name (branch name) is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "git_branch", map[string]interface{}{
		"session_id":  sessionID,
		"branch_name": branchName,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(TextResult{Message: result.Output})
}

// PushSession pushes the current branch to origin in a session's working directory.
func (e *Executor) PushSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("push-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" && len(remaining) > 0 {
		sessionID = remaining[0]
	}
	if sessionID == "" {
		return protocol.ErrResponse("session_id is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "git_push", map[string]interface{}{
		"session_id": sessionID,
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing result: %v", err))
	}

	return protocol.OKResponse(TextResult{Message: result.Output})
}
