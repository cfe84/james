package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/memory"
	"james/moneypenny/pkg/store"
)

const scheduleSystemPromptSuffix = `

You can schedule a follow-up task by including a tag in your response:
<schedule at="2026-03-07T15:00:00Z">Your follow-up prompt here</schedule>
The "at" attribute accepts RFC3339 timestamps or relative durations like "+2h", "+30m".
When you schedule a follow-up, the system will automatically send that prompt to you at the specified time.
Use this to set reminders, check on long-running processes, or break work into timed phases.`

// Handler processes commands and returns responses.
type Handler struct {
	store             *store.Store
	runner            *agent.Runner
	version           string
	dataDir           string // moneypenny data root; used for per-session storage
	vlog              func(string, ...interface{})
	updateStatusFunc  func() envelope.UpdateStatusResponse
	triggerUpdateFunc func() bool                  // returns true if check was queued
	notifyWriter      *envelope.NotificationWriter // for sending async notifications to hem
}

// resultCallback is called when an async agent execution completes.
// It can be set to push notifications, but by default is nil.
type resultCallback func(sessionID, response string, err error)

// New creates a new Handler with the given store, runner, and version string.
// dataDir is the moneypenny data root (e.g. ~/.config/james/moneypenny) and is
// used to allocate per-session persistent directories (sessions/<sessionID>/).
func New(s *store.Store, runner *agent.Runner, version, dataDir string) *Handler {
	h := &Handler{store: s, runner: runner, version: version, dataDir: dataDir, vlog: func(string, ...interface{}) {}}
	// Persist thinking and intermediate-text activity events as conversation
	// turns so the train of thought survives across reloads.
	runner.SetPersistentActivityFunc(func(sessionID, eventType, content string) {
		role := "thinking"
		if eventType == "text" {
			role = "agent_text"
		}
		_ = s.AddConversationTurn(sessionID, role, content)
	})
	return h
}

// sessionDir returns the per-session persistent directory under the data dir,
// creating it if it doesn't exist. Returns "" if dataDir or sessionID is empty,
// or if creation fails (caller should treat as "no session dir available").
func (h *Handler) sessionDir(sessionID string) string {
	if h.dataDir == "" || sessionID == "" {
		return ""
	}
	dir := filepath.Join(h.dataDir, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		h.vlog("sessionDir: failed to create %s: %v", dir, err)
		return ""
	}
	return dir
}

// memoryDir returns the per-session memory folder (<sessionDir>/memory). The
// agent reads and edits this folder directly with its native file tools; it is
// also the single source of truth backing the show/list/search/update/delete
// memory commands. Returns "" when no session dir is available.
func (h *Handler) memoryDir(sessionID string) string {
	sd := h.sessionDir(sessionID)
	if sd == "" {
		return ""
	}
	return filepath.Join(sd, "memory")
}

// attachmentsDir returns the per-session attachments folder
// (<sessionDir>/attachments), creating it if needed. Uploaded files (images,
// documents) are stored here — outside the working directory so they don't
// pollute the user's repo — and referenced by absolute path when invoking the
// agent. Returns "" when no session dir is available.
func (h *Handler) attachmentsDir(sessionID string) string {
	sd := h.sessionDir(sessionID)
	if sd == "" {
		return ""
	}
	dir := filepath.Join(sd, "attachments")
	if err := os.MkdirAll(dir, 0700); err != nil {
		h.vlog("attachmentsDir: failed to create %s: %v", dir, err)
		return ""
	}
	return dir
}

// SetLogger sets a verbose logger.
func (h *Handler) SetLogger(vlog func(string, ...interface{})) {
	h.vlog = vlog
}

// SetUpdateStatusFunc sets the function called to get update status from the updater.
func (h *Handler) SetUpdateStatusFunc(f func() envelope.UpdateStatusResponse) {
	h.updateStatusFunc = f
}

// SetTriggerUpdateFunc sets the function called to trigger an immediate update check.
// Returns true if a check was queued, false if one was already pending or auto-update
// is disabled.
func (h *Handler) SetTriggerUpdateFunc(f func() bool) {
	h.triggerUpdateFunc = f
}

// SetNotificationWriter sets the writer for sending async notifications.
func (h *Handler) SetNotificationWriter(nw *envelope.NotificationWriter) {
	h.notifyWriter = nw
	h.store.SetNotificationWriter(nw)
	h.runner.SetNotificationWriter(nw)
}

// AllSessionsIdle returns true if no sessions are in the "working" state.
// Implements updater.SessionChecker.
func (h *Handler) AllSessionsIdle() bool {
	sessions, err := h.store.ListSessions()
	if err != nil {
		h.vlog("allSessionsIdle: error listing sessions: %v", err)
		return false // err on the side of caution
	}
	for _, s := range sessions {
		if s.Status == store.StateWorking {
			return false
		}
	}
	return true
}

// Handle dispatches a command to the appropriate method handler.
// Returns a Response (never nil).
func (h *Handler) Handle(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	switch cmd.Method {
	case "create_session":
		return h.createSession(ctx, cmd)
	case "continue_session":
		return h.continueSession(ctx, cmd)
	case "list_sessions":
		return h.listSessions(ctx, cmd)
	case "get_session":
		return h.getSession(ctx, cmd)
	case "get_session_conversation":
		return h.getSessionConversation(ctx, cmd)
	case "queue_prompt":
		return h.queuePrompt(ctx, cmd)
	case "delete_session":
		return h.deleteSession(ctx, cmd)
	case "stop_session":
		return h.stopSession(ctx, cmd)
	case "update_session":
		return h.updateSession(ctx, cmd)
	case "import_session":
		return h.importSession(ctx, cmd)
	case "git_diff":
		return h.gitDiff(ctx, cmd)
	case "git_log":
		return h.gitLog(ctx, cmd)
	case "git_info":
		return h.gitInfo(ctx, cmd)
	case "git_show":
		return h.gitShow(ctx, cmd)
	case "git_commit":
		return h.gitCommit(ctx, cmd)
	case "git_branch":
		return h.gitBranch(ctx, cmd)
	case "git_push":
		return h.gitPush(ctx, cmd)
	case "execute_command":
		return h.executeCommand(ctx, cmd)
	case "list_directory":
		return h.listDirectory(ctx, cmd)
	case "transfer_file":
		return h.transferFile(ctx, cmd)
	case "save_attachment":
		return h.saveAttachment(ctx, cmd)
	case "schedule":
		return h.schedule(ctx, cmd)
	case "list_schedules":
		return h.listSchedules(ctx, cmd)
	case "cancel_schedule":
		return h.cancelSchedule(ctx, cmd)
	case "show_memory":
		return h.showMemory(ctx, cmd)
	case "list_memory":
		return h.listMemory(ctx, cmd)
	case "search_memory":
		return h.searchMemory(ctx, cmd)
	case "update_memory":
		return h.updateMemory(ctx, cmd)
	case "delete_memory":
		return h.deleteMemory(ctx, cmd)
	case "get_session_activity":
		return h.getSessionActivity(ctx, cmd)
	case "list_models":
		return h.listModels(ctx, cmd)
	case "get_version":
		return h.getVersion(cmd)
	case "check_agents":
		return h.checkAgents(cmd)
	case "update_status":
		return h.updateStatus(cmd)
	case "check_update":
		return h.checkUpdate(cmd)
	case "summarize_session":
		return h.summarizeSession(ctx, cmd)
	case "compact_session":
		return h.compactSessionCmd(ctx, cmd)
	case "distill_session":
		return h.distillSessionCmd(ctx, cmd)
	default:
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("unknown method: %s", cmd.Method))
	}
}

func (h *Handler) createSession(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.CreateSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	// Validate required fields.
	if data.Agent == "" || data.Prompt == "" || data.SessionID == "" || data.Name == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "agent, prompt, session_id, and name are required")
	}

	// Validate path exists.
	if _, err := os.Stat(data.Path); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, fmt.Sprintf("path does not exist: %s", data.Path))
	}

	// Check agent binary exists (PATH or well-known install locations).
	if _, err := agent.FindAgent(data.Agent); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrAgentNotFound, fmt.Sprintf("agent binary not found: %s", data.Agent))
	}

	systemPrompt := data.SystemPrompt

	compactionMode := data.CompactionMode
	if compactionMode == "" {
		compactionMode = store.CompactionCustom
	}

	// Create session in store.
	sess := &store.Session{
		SessionID:      data.SessionID,
		Name:           data.Name,
		Agent:          data.Agent,
		SystemPrompt:   systemPrompt,
		Model:          data.Model,
		Effort:         data.Effort,
		Yolo:           data.Yolo,
		Path:           data.Path,
		AgentSessionID: data.SessionID,
		CompactionMode: compactionMode,
	}
	if err := h.store.CreateSession(sess); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionAlreadyExists, fmt.Sprintf("session already exists: %s", data.SessionID))
	}

	// Set status to working.
	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateWorking); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}

	// Inherit the source session's memory folder when duplicating.
	// Validate the source is a real session in the store first — moneypenny
	// receives raw JSON, so this is the trust boundary that prevents a crafted
	// copy_memory_from value (e.g. "../../") from escaping the sessions dir.
	if data.CopyMemoryFrom != "" {
		if _, err := h.store.GetSession(data.CopyMemoryFrom); err != nil {
			h.vlog("copy_memory_from %q is not a known session: %v", data.CopyMemoryFrom, err)
		} else {
			srcMem := h.memoryDir(data.CopyMemoryFrom)
			// Ensure any legacy SQLite memory on the source is exported to files
			// first so the copy captures it.
			h.ensureMemoryMigrated(data.CopyMemoryFrom)
			dstMem := h.memoryDir(data.SessionID)
			if srcMem != "" && dstMem != "" {
				if err := memory.CopyTree(srcMem, dstMem); err != nil {
					h.vlog("copy memory %s -> %s: %v", data.CopyMemoryFrom, data.SessionID, err)
				}
			}
		}
	}

	// Notify that session is now working.
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventChatStatus, data.SessionID, map[string]string{
			"status": store.StateWorking,
			"reason": "agent_started",
		})
	}

	// Add user prompt to conversation.
	if err := h.store.AddConversationTurn(data.SessionID, "user", data.Prompt); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to add conversation turn: %v", err))
	}

	// Run agent asynchronously.
	go h.runAgent(data.SessionID, agent.RunParams{
		SessionID:    data.SessionID,
		Agent:        data.Agent,
		Prompt:       data.Prompt,
		SystemPrompt: systemPrompt,
		Model:        data.Model,
		Effort:       data.Effort,
		Yolo:         data.Yolo,
		Path:         data.Path,
		Resume:       false,
	})

	return envelope.SuccessResponse(cmd.RequestID, envelope.CreateSessionResponse{
		SessionID: data.SessionID,
	})
}

func (h *Handler) continueSession(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ContinueSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	// Validate required fields.
	if data.SessionID == "" || data.Prompt == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id and prompt are required")
	}

	// Get session from store.
	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Check status is idle.
	if sess.Status != store.StateIdle {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotIdle, fmt.Sprintf("session is not idle: %s", sess.Status))
	}

	// Update status to working.
	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateWorking); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}

	// Notify that session is now working.
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventChatStatus, data.SessionID, map[string]string{
			"status": store.StateWorking,
			"reason": "agent_started",
		})
	}

	// Build the effective prompt: when attachments accompany the prompt, append
	// a human-readable addendum listing their absolute paths. This serves two
	// purposes: the persisted user turn reflects what was sent, and (for Claude,
	// which has no native attachment flag) it tells the agent where to find the
	// files it has been granted access to via --add-dir.
	prompt := data.Prompt
	if len(data.Attachments) > 0 {
		prompt += attachmentPromptAddendum(data.Attachments)
	}

	// Add user prompt to conversation.
	if err := h.store.AddConversationTurn(data.SessionID, "user", prompt); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to add conversation turn: %v", err))
	}

	// Resolve the effective model/effort: a per-prompt override (when supplied)
	// takes precedence over the session's stored default.
	effModel := sess.Model
	if data.Model != "" {
		effModel = data.Model
	}
	effEffort := sess.Effort
	if data.Effort != "" {
		effEffort = data.Effort
	}

	// If custom compaction is enabled and the context has grown past the
	// threshold, compact first, seeding the fresh underlying session with this
	// prompt (automatic compaction provides the next prompt).
	if h.shouldCompact(sess) {
		h.vlog("context threshold reached for session %s; auto-compacting before continue", data.SessionID)
		go h.runCompaction(data.SessionID, prompt, effModel, effEffort)
		return envelope.SuccessResponse(cmd.RequestID, envelope.ContinueSessionResponse{
			SessionID: data.SessionID,
		})
	}

	// Run agent asynchronously with Resume=true.
	go h.runAgent(data.SessionID, agent.RunParams{
		SessionID:    data.SessionID,
		Agent:        sess.Agent,
		Prompt:       prompt,
		SystemPrompt: sess.SystemPrompt,
		Model:        effModel,
		Effort:       effEffort,
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Resume:       true,
		Attachments:  data.Attachments,
	})

	return envelope.SuccessResponse(cmd.RequestID, envelope.ContinueSessionResponse{
		SessionID: data.SessionID,
	})
}

func (h *Handler) queuePrompt(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ContinueSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" || data.Prompt == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id and prompt are required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	if err := h.store.QueuePrompt(data.SessionID, data.Prompt, data.Model, data.Effort, ""); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to queue prompt: %v", err))
	}

	queueLen, _ := h.store.QueueLength(data.SessionID)
	h.vlog("queued prompt for session %s (queue length: %d)", data.SessionID, queueLen)

	return envelope.SuccessResponse(cmd.RequestID, map[string]interface{}{
		"session_id":   data.SessionID,
		"queued":       true,
		"queue_length": queueLen,
	})
}

// isSessionNotFoundErr returns true if the agent error indicates that the
// underlying agent-side session was lost / never existed (so we should
// compact our history and start fresh on the agent side).
func isSessionNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"No conversation found with session ID",
		"No session, task, or name matched",
		"session not found",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// CompactSession asks the session's configured agent to produce a summary of
// the conversation history stored in moneypenny. Reusable for recovery (when
// the agent-side session is gone) and future uses (e.g. explicit user-driven
// compaction). Returns "" if there's no history to compact.
func (h *Handler) CompactSession(ctx context.Context, sessionID string) (string, int, error) {
	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("get session: %w", err)
	}
	if sess == nil {
		return "", 0, fmt.Errorf("session not found: %s", sessionID)
	}
	turns, err := h.store.GetConversation(sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("get conversation: %w", err)
	}
	if len(turns) == 0 {
		return "", 0, nil
	}

	// Build a clean transcript. Skip thinking/agent_text noise — keep only
	// user, assistant, and system role turns.
	transcript := cleanTranscript(turns)

	prompt := fmt.Sprintf(`Below is the full history of a prior conversation. Provide a comprehensive summary that captures:
- The original task or question
- Key decisions made and their rationale
- Important context: file paths, names, conventions, learnings
- Current state — what was being worked on at the end
- Any pending actions or unresolved questions

Be detailed enough that the conversation can be RESUMED with this summary alone as context. Output ONLY the summary text, no preamble or meta-commentary.

Conversation:
%s`, transcript)

	// For copilot, system-prompt-via-AGENTS.md isn't available for one-shots,
	// so prepend any system prompt directly into the user prompt.
	if sess.Agent == "copilot" && sess.SystemPrompt != "" {
		prompt = "Context for this task:\n" + sess.SystemPrompt + "\n\n---\n\n" + prompt
	}

	summary, err := h.runner.RunOneShot(ctx, agent.RunParams{
		Agent:        sess.Agent,
		Model:        sess.Model,
		Effort:       sess.Effort,
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Prompt:       prompt,
		SystemPrompt: sess.SystemPrompt, // used by claude one-shot
	})
	return summary, len(turns), err
}

// summarizeSession is the user-facing dispatch for summarize_session.
// Returns the summary as a SummarizeSessionResponse. Empty summary is a
// successful response (e.g. the session exists but has no history yet).
func (h *Handler) summarizeSession(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SummarizeSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	// Bound the summarization call to a generous timeout; it spawns a one-shot
	// agent that walks the entire transcript.
	sumCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	summary, turnCount, err := h.CompactSession(sumCtx, data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.SummarizeSessionResponse{
		SessionID: data.SessionID,
		Summary:   summary,
		TurnCount: turnCount,
	})
}

// runAgent executes the agent in the background, updating the store when done.
// After completion, it checks the prompt queue and auto-continues if there are queued prompts.
func (h *Handler) runAgent(sessionID string, params agent.RunParams) {
	// Inject the file-based memory instructions plus a compact, body-less outline
	// of the session's memory tree into the system prompt at runtime. The agent
	// edits its memory folder directly with its native file tools, so it always
	// sees an up-to-date map of what it knows and where to write. Memory is
	// skipped for agent/permission combinations that can't write it without an
	// interactive prompt (non-yolo Copilot — see agent.MemoryEnabled).
	if agent.MemoryEnabled(params.Agent, params.Yolo) {
		h.ensureMemoryMigrated(sessionID)
		if memDir := h.memoryDir(sessionID); memDir != "" {
			params.MemoryDir = memDir
			params.SystemPrompt += memorySystemPrompt(memDir)
		}
	}

	// Provide the per-session persistent directory to the agent runner so it
	// can use it for things like copilot's COPILOT_CUSTOM_INSTRUCTIONS_DIRS.
	if params.SessionDir == "" {
		params.SessionDir = h.sessionDir(sessionID)
	}

	// Resolve the underlying agent session id (decoupled from the James
	// session id to support custom-compaction substitution). Falls back to the
	// James session id for sessions created before this column existed.
	if params.AgentSessionID == "" {
		if s, err := h.store.GetSession(sessionID); err == nil && s != nil && s.AgentSessionID != "" {
			params.AgentSessionID = s.AgentSessionID
		}
	}

	ctx := context.Background()
	result, err := h.runner.Run(ctx, params)

	// Recovery: if the agent reports its session is gone, compact our history
	// and retry as a fresh agent-side session with the summary as context.
	if err != nil && isSessionNotFoundErr(err) {
		h.vlog("session %s gone on agent side; compacting history and retrying", sessionID)
		_ = h.store.AddConversationTurn(sessionID, "system",
			"Underlying agent session was lost. Compacting prior conversation and starting fresh…")
		compactCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		summary, turnCount, sumErr := h.CompactSession(compactCtx, sessionID)
		cancel()
		if sumErr != nil {
			h.vlog("compaction failed for session %s: %v", sessionID, sumErr)
		} else if summary == "" && turnCount > 0 {
			// History existed but the summarizer returned nothing (e.g. a
			// retired model). Surface it so the fresh retry's lack of prior
			// context is explained rather than silently lost.
			h.vlog("compaction returned empty summary for session %s despite %d stored turns; retrying without prior context", sessionID, turnCount)
			_ = h.store.AddConversationTurn(sessionID, "system",
				"Could not summarize prior conversation before restarting; continuing without earlier context.")
		}
		// Retry as a CREATE against a fresh underlying agent session id (the old
		// one is gone; reusing it can collide on agents that treat --session-id
		// as create-only).
		newAgentID := newAgentSessionID()
		_ = h.store.SetAgentSessionID(sessionID, newAgentID)
		params.AgentSessionID = newAgentID
		params.Resume = false
		if summary != "" {
			params.SystemPrompt += "\n\n<prior-session-summary>\n" + summary + "\n</prior-session-summary>"
		}
		result, err = h.runner.Run(ctx, params)
	}
	if err != nil {
		h.vlog("agent error for session %s: %v", sessionID, err)
		// Surface the error as a conversation turn so the user can see it.
		errMsg := fmt.Sprintf("Agent failed to execute: %v", err)
		_ = h.store.AddConversationTurn(sessionID, "system", errMsg)
		_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)

		// Notify hem that session became idle after error.
		if h.notifyWriter != nil {
			_ = h.notifyWriter.Send(envelope.EventSessionStateChanged, sessionID, map[string]string{
				"status": store.StateIdle,
				"reason": "agent_error",
			})
			_ = h.notifyWriter.Send(envelope.EventChatStatus, sessionID, map[string]string{
				"status": store.StateIdle,
				"reason": "agent_error",
			})
		}
		return
	}

	// Parse and create any <schedule> tags from agent output.
	responseText := h.parseAndCreateSchedules(sessionID, result.Text)

	// Dedupe (Claude only): the Claude streaming parser saves each text block
	// as an intermediate `agent_text` turn, and the final `result` event often
	// duplicates the last block. Drop that trailing duplicate before adding the
	// assistant turn. The Copilot parser persists only *preamble* narration as
	// `agent_text` (never the reply text), so its reply won't match a trailing
	// agent_text turn and this is effectively a no-op for Copilot sessions.
	if dropped, _ := h.store.DeleteLastTurnIfMatches(sessionID, "agent_text", responseText); dropped {
		h.vlog("dedup: removed trailing agent_text turn matching final response for session %s", sessionID)
	}

	// Skip storing an empty assistant turn: when the agent's last action is
	// a tool call (or it otherwise finishes without producing a final text
	// block), result.Text is empty. The chain-of-thought already captured
	// whatever the agent produced via agent_text turns, so an empty
	// assistant turn would just render as "(empty)" in the chat.
	if strings.TrimSpace(responseText) != "" {
		if err := h.store.AddConversationTurn(sessionID, "assistant", responseText); err != nil {
			h.vlog("failed to add conversation turn for session %s: %v", sessionID, err)
		}
	} else {
		h.vlog("skipping empty assistant turn for session %s (agent produced no final text)", sessionID)
	}

	h.vlog("agent completed for session %s", sessionID)

	// Record context usage for this turn so custom compaction can be triggered
	// at the configured threshold and clients can display usage. Claude reports
	// real token counts and its model's window; Copilot reports neither, so we
	// estimate from the stored transcript and fall back to a burned-in window.
	h.recordContextUsage(sessionID, params, result)

	// Check for queued prompts before going idle. Drain one override-group at a
	// time: prompts sharing the same per-prompt model/effort override are
	// processed together, and the next group (if any) is handled by the
	// recursive drain at the end of this run. This honors distinct overrides
	// without re-inserting prompts, so queue order is always preserved.
	group, err := h.store.DrainQueueGroup(sessionID)
	if err != nil {
		h.vlog("failed to drain queue for session %s: %v", sessionID, err)
	}

	if len(group) > 0 {
		h.vlog("processing %d queued prompt(s) for session %s", len(group), sessionID)

		// Re-fetch session for latest settings.
		sess, err := h.store.GetSession(sessionID)
		if err != nil || sess == nil {
			h.vlog("failed to get session %s for queued continuation: %v", sessionID, err)
			_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)
			return
		}

		first := group[0]

		// Process each prompt in the group as its own conversation turn.
		texts := make([]string, 0, len(group))
		for _, qp := range group {
			role := "user"
			if qp.Source == "scheduled" {
				role = "scheduled"
			}
			if err := h.store.AddConversationTurn(sessionID, role, qp.Prompt); err != nil {
				h.vlog("failed to add queued conversation turn for session %s: %v", sessionID, err)
			}
			texts = append(texts, qp.Prompt)
		}

		// A per-prompt override (when present) wins over the session default.
		effModel := sess.Model
		if first.Model != "" {
			effModel = first.Model
		}
		effEffort := sess.Effort
		if first.Effort != "" {
			effEffort = first.Effort
		}

		// Continue with this group's prompts joined for the agent.
		combinedPrompt := strings.Join(texts, "\n")

		// If custom compaction is enabled and the context has grown past the
		// threshold, compact before handling this group, seeding the fresh
		// underlying session with these prompts (automatic compaction provides
		// the next prompt rather than "await instructions").
		if h.shouldCompact(sess) {
			h.vlog("context threshold reached for session %s; auto-compacting before queued group", sessionID)
			h.runCompaction(sessionID, combinedPrompt, effModel, effEffort)
			return
		}

		h.runAgent(sessionID, agent.RunParams{
			SessionID:    sessionID,
			Agent:        sess.Agent,
			Prompt:       combinedPrompt,
			SystemPrompt: sess.SystemPrompt,
			Model:        effModel,
			Effort:       effEffort,
			Yolo:         sess.Yolo,
			Path:         sess.Path,
			Resume:       true,
		})
		return
	}

	if err := h.store.UpdateSessionStatus(sessionID, store.StateIdle); err != nil {
		h.vlog("failed to update status for session %s: %v", sessionID, err)
	}

	// Notify hem that session became idle after completion.
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventSessionStateChanged, sessionID, map[string]string{
			"status": store.StateIdle,
			"reason": "completed",
		})
		_ = h.notifyWriter.Send(envelope.EventChatStatus, sessionID, map[string]string{
			"status": store.StateIdle,
			"reason": "completed",
		})
	}
}

func (h *Handler) listSessions(_ context.Context, cmd *envelope.Command) *envelope.Response {
	sessions, err := h.store.ListSessions()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to list sessions: %v", err))
	}

	infos := make([]envelope.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		info := envelope.SessionInfo{
			SessionID: s.SessionID,
			Name:      s.Name,
			Status:    s.Status,
			Agent:     s.Agent,
			CreatedAt: s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		// Use the last conversation turn as last_accessed, falling back to
		// the session's updated_at (which tracks status changes like working→idle).
		if ts, err := h.store.GetSessionTimestamps(s.SessionID); err == nil && ts != nil {
			info.LastAccessed = ts.LastTurn.UTC().Format("2006-01-02T15:04:05Z")
		}
		if info.LastAccessed == "" {
			info.LastAccessed = s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		infos = append(infos, info)
	}

	return envelope.SuccessResponse(cmd.RequestID, infos)
}

func (h *Handler) getSession(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	detail := envelope.SessionDetail{
		SessionID:      sess.SessionID,
		Name:           sess.Name,
		Status:         sess.Status,
		Agent:          sess.Agent,
		SystemPrompt:   sess.SystemPrompt,
		Model:          sess.Model,
		Effort:         sess.Effort,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Memory:         sess.Memory,
		CompactionMode: sess.CompactionMode,
		ContextTokens:  sess.ContextTokens,
		ContextWindow:  sess.ContextWindow,
	}

	if ts, err := h.store.GetSessionTimestamps(data.SessionID); err == nil && ts != nil {
		detail.LastAccessed = ts.LastTurn.UTC().Format("2006-01-02T15:04:05Z")
	}

	return envelope.SuccessResponse(cmd.RequestID, detail)
}

func (h *Handler) getSessionConversation(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.GetConversationData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	total, err := h.store.GetConversationCount(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to count conversation: %v", err))
	}

	var turns []*store.ConversationTurn
	if data.All {
		turns, err = h.store.GetConversation(data.SessionID)
	} else {
		count := data.Count
		if count <= 0 {
			count = 10
		}
		turns, err = h.store.GetConversationPaginated(data.SessionID, count, data.From)
	}
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get conversation: %v", err))
	}

	conversation := make([]envelope.ConversationTurn, 0, len(turns))
	for _, t := range turns {
		conversation = append(conversation, envelope.ConversationTurn{
			Role:      t.Role,
			Content:   t.Content,
			CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.SessionConversation{
		SessionID:    data.SessionID,
		Conversation: conversation,
		Total:        total,
	})
}

func (h *Handler) getSessionActivity(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	events := h.runner.GetActivity(data.SessionID)
	// Convert agent.ActivityEvent to envelope.ActivityEvent.
	activity := make([]envelope.ActivityEvent, len(events))
	for i, ev := range events {
		activity[i] = envelope.ActivityEvent{
			Type:      ev.Type,
			Summary:   ev.Summary,
			Timestamp: ev.Timestamp,
		}
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.SessionActivityResponse{
		SessionID: data.SessionID,
		Activity:  activity,
	})
}

func (h *Handler) deleteSession(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// If working, stop the agent process (ignore error if already gone).
	if sess.Status == store.StateWorking {
		_ = h.runner.Stop(data.SessionID)
	}

	if err := h.store.DeleteSession(data.SessionID); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to delete session: %v", err))
	}

	// Clean up the per-session persistent dir (best-effort).
	if h.dataDir != "" {
		dir := filepath.Join(h.dataDir, "sessions", data.SessionID)
		if err := os.RemoveAll(dir); err != nil {
			h.vlog("deleteSession: failed to remove session dir %s: %v", dir, err)
		}
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}

func (h *Handler) updateSession(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.UpdateSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	// Validate path if provided.
	if data.Path != nil && *data.Path != "" {
		if _, err := os.Stat(*data.Path); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, fmt.Sprintf("path does not exist: %s", *data.Path))
		}
	}

	if err := h.store.UpdateSessionFields(data.SessionID, data.Name, data.SystemPrompt, data.Model, data.Effort, data.Path, data.CompactionMode, data.Yolo); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update session: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}

// MigrateMemoryToFiles exports every session's legacy SQLite memory into its
// file-based memory folder, idempotently (skips sessions whose folder already
// has content). Returns the number of sessions exported. Intended to run once
// at moneypenny startup.
//
// TODO(2026-06-12): remove this migration helper and the SQLite memory store.
func (h *Handler) MigrateMemoryToFiles() int {
	sessions, err := h.store.ListSessions()
	if err != nil {
		h.vlog("memory migration: list sessions: %v", err)
		return 0
	}
	migrated := 0
	for _, s := range sessions {
		memDir := h.memoryDir(s.SessionID)
		if memDir == "" || !memory.IsEmpty(memDir) {
			continue
		}
		count, _ := h.store.MemoryNodeCount(s.SessionID)
		blob, _ := h.store.GetMemory(s.SessionID)
		if count == 0 && strings.TrimSpace(blob) == "" {
			continue
		}
		if err := h.exportLegacyMemory(s.SessionID, memDir); err != nil {
			h.vlog("memory migration for session %s: %v", s.SessionID, err)
			continue
		}
		migrated++
	}
	return migrated
}

// ensureMemoryMigrated lazily exports a session's legacy SQLite memory (the node
// tree, or the older flat blob) into the file-based memory folder on first
// access. Idempotent: it no-ops once the folder has any content.
//
// TODO(2026-06-12): remove this migration shim and the SQLite memory store once
// all active sessions have been exported to the filesystem.
func (h *Handler) ensureMemoryMigrated(sessionID string) {
	memDir := h.memoryDir(sessionID)
	if memDir == "" || !memory.IsEmpty(memDir) {
		return
	}
	if err := h.exportLegacyMemory(sessionID, memDir); err != nil {
		h.vlog("memory export for session %s: %v", sessionID, err)
	}
}

// exportLegacyMemory writes the session's SQLite memory nodes out as README.md
// files under memDir. Title/Description (which the file model folds into the
// body) are preserved by prepending them as a heading + summary.
func (h *Handler) exportLegacyMemory(sessionID, memDir string) error {
	// Fold the oldest flat blob into a single node first, matching prior behavior.
	if _, err := h.store.MigrateLegacyMemory(sessionID); err != nil {
		h.vlog("legacy blob migration for session %s: %v", sessionID, err)
	}
	nodes, err := h.store.ListMemoryNodes(sessionID)
	if err != nil {
		return err
	}
	var firstErr error
	for _, n := range nodes {
		body := n.Body
		if n.Title != "" || n.Description != "" {
			var hb strings.Builder
			if n.Title != "" {
				hb.WriteString("# " + n.Title + "\n\n")
			}
			if n.Description != "" {
				hb.WriteString(n.Description + "\n\n")
			}
			hb.WriteString(body)
			body = hb.String()
		}
		if _, err := memory.Set(memDir, n.Path, body); err != nil {
			h.vlog("export memory node %q for session %s: %v", n.Path, sessionID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	// Surface a partial-export failure so the caller does not mark the session
	// migrated; it will be retried (lazily or on next startup) until every node
	// is written, avoiding silent data loss when the SQLite store is removed.
	return firstErr
}

// rootReadmeTemplate seeds a fresh memory folder so the agent has a starting
// index to extend.
const rootReadmeTemplate = `# Session Memory

Persistent memory for this agent session. Organize knowledge hierarchically:
each topic is a folder with its own README.md that serves as both the topic note
and an index of its sub-topics. Read this file first, then drill into folders.

## Index

(Add links to topic folders here as you create them.)
`

// seedRootReadme writes the root README.md if the folder has no root note yet.
func seedRootReadme(memDir string) {
	readme := filepath.Join(memDir, "README.md")
	if _, err := os.Stat(readme); err == nil {
		return
	}
	_ = os.MkdirAll(memDir, 0o755)
	_ = os.WriteFile(readme, []byte(rootReadmeTemplate), 0o644)
}

// memorySystemPrompt builds the system-prompt block that points the agent at its
// file-based memory folder. It ensures the folder exists, seeds a root README on
// first use, and appends an up-to-date outline of the tree.
func memorySystemPrompt(memDir string) string {
	_ = os.MkdirAll(memDir, 0o755)
	seedRootReadme(memDir)
	var b strings.Builder
	b.WriteString("\n\n<session-memory>\n")
	b.WriteString("You have a persistent MEMORY FOLDER at:\n  ")
	b.WriteString(memDir)
	b.WriteString("\nThis folder is yours to read and edit with your normal file tools. It survives this session's compactions and restarts, so use it as your long-term knowledge base — record the task, key decisions and rationale, conventions, important paths/names, current state, and pending actions.\n\n")
	b.WriteString("Structure (uniform hierarchy):\n")
	b.WriteString("- Every topic is a FOLDER containing a README.md that is both that topic's note and an index of its sub-topics.\n")
	b.WriteString("- The root note is " + filepath.Join(memDir, "README.md") + " — keep a high-level overview and a table of contents there.\n")
	b.WriteString("- Put detail in nested topic folders (e.g. project/conventions/README.md). Keep each note focused; update existing notes instead of duplicating; summarize in parent READMEs so nothing is lost.\n")
	if outline, err := memory.Outline(memDir); err == nil && strings.TrimSpace(outline) != "" {
		b.WriteString("\nCurrent memory tree:\n")
		b.WriteString(outline)
		b.WriteString("\n")
	}
	b.WriteString("</session-memory>\n")
	return b.String()
}

// memNodePayload converts a file-based memory node to its protocol payload.
func memNodePayload(n *memory.Node, withBody bool) envelope.MemoryNodePayload {
	p := envelope.MemoryNodePayload{Path: n.Path, Description: n.Description}
	if withBody {
		p.Body = n.Body
	}
	return p
}

func (h *Handler) showMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ShowMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	memDir := h.memoryDir(data.SessionID)
	if memDir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no session directory available")
	}
	h.ensureMemoryMigrated(data.SessionID)

	resp := envelope.ShowMemoryResponse{SessionID: data.SessionID, Path: data.Path}
	if strings.TrimSpace(data.Path) == "" {
		outline, err := memory.Outline(memDir)
		if err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
		}
		resp.Outline = outline
		// Surface the root README itself (with body) so clients can show and
		// edit the root note. It has an empty path, which the node branch below
		// can't be reached for, so include it here in the overview response.
		if root, gerr := memory.Get(memDir, ""); gerr == nil && root != nil {
			payload := memNodePayload(root, true)
			resp.Node = &payload
		}
		nodes, err := memory.List(memDir)
		if err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
		}
		for _, n := range nodes {
			// Skip the root here (empty path) — it's returned as resp.Node
			// above; resp.Nodes lists only the named, drill-into child nodes.
			if n.Path == "" {
				continue
			}
			resp.Nodes = append(resp.Nodes, memNodePayload(n, false))
		}
		return envelope.SuccessResponse(cmd.RequestID, resp)
	}

	path, err := memory.NormalizePath(data.Path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	node, err := memory.Get(memDir, path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	if node == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("no memory node at %q", path))
	}
	payload := memNodePayload(node, true)
	resp.Node = &payload
	children, err := memory.Children(memDir, path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	for _, c := range children {
		resp.Children = append(resp.Children, memNodePayload(c, false))
	}
	return envelope.SuccessResponse(cmd.RequestID, resp)
}

func (h *Handler) listMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ListMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	memDir := h.memoryDir(data.SessionID)
	if memDir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no session directory available")
	}
	h.ensureMemoryMigrated(data.SessionID)

	parent := ""
	if strings.TrimSpace(data.Path) != "" {
		p, err := memory.NormalizePath(data.Path)
		if err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
		}
		parent = p
	}
	children, err := memory.Children(memDir, parent)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	resp := envelope.ListMemoryResponse{SessionID: data.SessionID, Path: parent}
	for _, c := range children {
		resp.Children = append(resp.Children, memNodePayload(c, false))
	}
	return envelope.SuccessResponse(cmd.RequestID, resp)
}

func (h *Handler) searchMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SearchMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	if strings.TrimSpace(data.Query) == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "query is required")
	}
	memDir := h.memoryDir(data.SessionID)
	if memDir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no session directory available")
	}
	h.ensureMemoryMigrated(data.SessionID)

	nodes, err := memory.Search(memDir, data.Query)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	resp := envelope.SearchMemoryResponse{SessionID: data.SessionID, Query: data.Query}
	for _, n := range nodes {
		resp.Results = append(resp.Results, memNodePayload(n, true))
	}
	return envelope.SuccessResponse(cmd.RequestID, resp)
}

func (h *Handler) updateMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.UpdateMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	if strings.TrimSpace(data.Path) == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "path is required")
	}
	memDir := h.memoryDir(data.SessionID)
	if memDir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no session directory available")
	}
	h.ensureMemoryMigrated(data.SessionID)

	// Body is authoritative for the file model. For backward compatibility with
	// callers that still split Title/Description from Body, fold them into the
	// note so nothing is lost.
	body := data.Body
	if data.Title != "" || data.Description != "" {
		var hb strings.Builder
		if data.Title != "" {
			hb.WriteString("# " + data.Title + "\n\n")
		}
		if data.Description != "" {
			hb.WriteString(data.Description + "\n\n")
		}
		hb.WriteString(body)
		body = hb.String()
	}
	path, err := memory.Set(memDir, data.Path, body)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.MemoryWriteResponse{
		SessionID: data.SessionID,
		Path:      path,
	})
}

func (h *Handler) deleteMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.DeleteMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	if strings.TrimSpace(data.Path) == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "path is required")
	}
	memDir := h.memoryDir(data.SessionID)
	if memDir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no session directory available")
	}
	h.ensureMemoryMigrated(data.SessionID)

	deleted, err := memory.Delete(memDir, data.Path, data.Recursive)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	path, _ := memory.NormalizePath(data.Path)
	return envelope.SuccessResponse(cmd.RequestID, envelope.MemoryWriteResponse{
		SessionID: data.SessionID,
		Path:      path,
		Deleted:   deleted,
	})
}

func (h *Handler) executeCommand(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ExecuteCommandData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.Command == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "command is required")
	}

	if data.Path != "" {
		if _, err := os.Stat(data.Path); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, fmt.Sprintf("path does not exist: %s", data.Path))
		}
	}

	var shellCmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shellCmd = exec.Command("cmd", "/C", data.Command)
	} else {
		shellCmd = exec.Command("sh", "-c", data.Command)
	}
	if data.Path != "" {
		shellCmd.Dir = data.Path
	}
	output, err := shellCmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to execute command: %v", err))
		}
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.ExecuteCommandResponse{
		Output:   string(output),
		ExitCode: exitCode,
	})
}

func (h *Handler) getVersion(cmd *envelope.Command) *envelope.Response {
	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"version": h.version})
}

func (h *Handler) checkAgents(cmd *envelope.Command) *envelope.Response {
	knownAgents := []string{"claude", "copilot"}
	var agents []envelope.AgentAvailability
	for _, name := range knownAgents {
		a := envelope.AgentAvailability{Name: name}
		if path, err := agent.FindAgent(name); err == nil {
			a.Found = true
			a.Path = path
		}
		agents = append(agents, a)
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.CheckAgentsResponse{Agents: agents})
}

func (h *Handler) updateStatus(cmd *envelope.Command) *envelope.Response {
	if h.updateStatusFunc == nil {
		return envelope.SuccessResponse(cmd.RequestID, envelope.UpdateStatusResponse{
			CurrentVersion: h.version,
			Status:         "disabled",
		})
	}
	return envelope.SuccessResponse(cmd.RequestID, h.updateStatusFunc())
}

func (h *Handler) checkUpdate(cmd *envelope.Command) *envelope.Response {
	if h.triggerUpdateFunc == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "auto-update is disabled on this moneypenny")
	}
	queued := h.triggerUpdateFunc()
	return envelope.SuccessResponse(cmd.RequestID, envelope.CheckUpdateResponse{
		Queued: queued,
	})
}

func (h *Handler) listModels(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ListModelsData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	agentName := data.Agent
	if agentName == "" {
		agentName = "claude"
	}

	var models []envelope.ModelInfo
	switch agentName {
	case "claude":
		models = claudeModels()
	case "copilot":
		models = copilotModels()
	default:
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("unknown agent: %s", agentName))
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.ListModelsResponse{
		Agent:  agentName,
		Models: models,
	})
}

// claudeModels returns known Claude model aliases.
// Claude CLI doesn't have a model listing command, so we use known aliases.
func claudeModels() []envelope.ModelInfo {
	return []envelope.ModelInfo{
		{Name: "sonnet", Value: "sonnet"},
		{Name: "opus", Value: "opus"},
		{Name: "haiku", Value: "haiku"},
	}
}

// Copilot model cache (querying is slow, ~10-20s).
var (
	copilotModelCache     []envelope.ModelInfo
	copilotModelCacheTime time.Time
	copilotModelCacheTTL  = 24 * time.Hour
)

// copilotModelLogRe matches each "[id,Display Name]" pair on copilot's
// debug-level "Listed models:" log line.
var copilotModelLogRe = regexp.MustCompile(`\[([^,\]]+),([^\]]*)\]`)

// copilotModels queries copilot for its available models. Copilot has no
// non-interactive "list models" command, so we run a trivial prompt with
// debug logging enabled and parse the authoritative "Listed models:" line
// the CLI emits after fetching /models (the real, complete list — dozens of
// entries). If that parse fails we fall back to parsing the model's textual
// answer to a "list the identifiers" prompt (older copilot builds, or a log
// format change). Results are cached to avoid repeated slow queries.
func copilotModels() []envelope.ModelInfo {
	if len(copilotModelCache) > 0 && time.Since(copilotModelCacheTime) < copilotModelCacheTTL {
		log.Printf("copilot models: returning %d cached models (age: %v)", len(copilotModelCache), time.Since(copilotModelCacheTime))
		return copilotModelCache
	}
	path, err := agent.FindAgent("copilot")
	if err != nil {
		log.Printf("copilot models: copilot not found: %v", err)
		return nil
	}

	log.Printf("copilot models: querying copilot at %s", path)

	// Capture copilot's debug logs in a throwaway directory we can parse and
	// then discard.
	logDir, err := os.MkdirTemp("", "copilot-models-")
	if err != nil {
		log.Printf("copilot models: failed to create log dir: %v", err)
		return nil
	}
	defer os.RemoveAll(logDir)

	// Run a trivial prompt. --available-tools '' prevents tool use (no
	// permission prompts, faster). --model auto lets copilot pick a current
	// model so the query can't fail because a pinned identifier was retired.
	// --log-level debug + --log-dir make the CLI write the "Listed models:"
	// line we parse below.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path,
		"-p", "List the model identifiers available for --model. One per line. No other text, no markdown formatting.",
		"--output-format", "text",
		"--available-tools", "",
		"--model", "auto",
		"--log-level", "debug",
		"--log-dir", logDir,
	)
	// Prepend agent's dir to PATH so its shebang (e.g. `env node`) finds
	// `node` in the same directory (nvm install layout).
	cmd.Env = agent.PrependToPath(os.Environ(), filepath.Dir(path))
	out, runErr := cmd.Output()
	if runErr != nil {
		// Don't bail yet: the model list is fetched and logged during startup,
		// before the completion runs, so the log may hold the full list even
		// when the prompt itself failed.
		log.Printf("copilot models: query exited with error (will still parse logs): %v", runErr)
	}

	// Primary: parse the authoritative debug-log model list.
	if models := parseCopilotModelLog(logDir); len(models) > 0 {
		copilotModelCache = models
		copilotModelCacheTime = time.Now()
		log.Printf("copilot models: parsed %d models from debug log", len(models))
		return models
	}

	// Fallback: parse the model's textual answer (older copilot, or a changed
	// log format). Only meaningful if the prompt actually completed.
	if runErr == nil {
		log.Printf("copilot models: debug log empty, falling back to stdout (%d bytes)", len(out))
		var models []envelope.ModelInfo
		for _, line := range strings.Split(string(out), "\n") {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			// Stop at the summary footer (lines starting with "Total" or similar).
			if strings.HasPrefix(name, "Total ") || strings.HasPrefix(name, "API ") ||
				strings.HasPrefix(name, "Breakdown ") {
				break
			}
			models = append(models, envelope.ModelInfo{Name: name, Value: name})
		}
		if len(models) > 0 {
			copilotModelCache = models
			copilotModelCacheTime = time.Now()
			log.Printf("copilot models: cached %d models (stdout fallback)", len(models))
			return models
		}
	}

	log.Printf("copilot models: no models parsed")
	return nil
}

// parseCopilotModelLog scans copilot's debug log files in logDir for the
// "Listed models: [id,Display Name], ..." line and returns the chat models it
// lists, in order, de-duplicated by identifier. Embedding models (which aren't
// valid --model values) are skipped.
func parseCopilotModelLog(logDir string) []envelope.ModelInfo {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil
	}
	var listLine string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(logDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if idx := strings.Index(line, "Listed models:"); idx >= 0 {
				listLine = line[idx+len("Listed models:"):]
				break
			}
		}
		if listLine != "" {
			break
		}
	}
	if listLine == "" {
		return nil
	}

	var models []envelope.ModelInfo
	seen := make(map[string]bool)
	for _, m := range copilotModelLogRe.FindAllStringSubmatch(listLine, -1) {
		id := strings.TrimSpace(m[1])
		if id == "" || seen[id] {
			continue
		}
		// Embedding models aren't selectable via --model; skip them.
		if strings.HasPrefix(id, "text-embedding") {
			continue
		}
		seen[id] = true
		display := strings.TrimSpace(m[2])
		if display == "" {
			display = id
		}
		models = append(models, envelope.ModelInfo{Name: display, Value: id})
	}
	return models
}

func (h *Handler) stopSession(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Try to kill the process if running; ignore errors (process may already be gone).
	_ = h.runner.Stop(data.SessionID)

	// Drain queued prompts so they don't restart the session.
	_, _ = h.store.DrainQueue(data.SessionID)

	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateIdle); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}

	h.vlog("force-stopped session %s (was %s)", data.SessionID, sess.Status)
	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}

func (h *Handler) importSession(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ImportSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" || data.Name == "" || data.Agent == "" || data.Path == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id, name, agent, and path are required")
	}

	// Create session in store.
	sess := &store.Session{
		SessionID:    data.SessionID,
		Name:         data.Name,
		Agent:        data.Agent,
		SystemPrompt: data.SystemPrompt,
		Yolo:         data.Yolo,
		Path:         data.Path,
	}
	if err := h.store.CreateSession(sess); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionAlreadyExists, fmt.Sprintf("session already exists: %s", data.SessionID))
	}

	// Import conversation turns.
	for _, turn := range data.Conversation {
		if err := h.store.AddConversationTurn(data.SessionID, turn.Role, turn.Content); err != nil {
			h.vlog("failed to add imported conversation turn for session %s: %v", data.SessionID, err)
		}
	}

	h.vlog("imported session %s with %d conversation turns", data.SessionID, len(data.Conversation))

	return envelope.SuccessResponse(cmd.RequestID, map[string]interface{}{
		"session_id": data.SessionID,
		"turns":      len(data.Conversation),
	})
}

func (h *Handler) gitDiff(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	// Get session from store to find its working directory.
	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Run git diff (unstaged changes).
	diffCmd := exec.Command("git", "diff")
	diffCmd.Dir = sess.Path
	unstaged, err := diffCmd.Output()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to run git diff: %v", err))
	}

	// Run git diff --cached (staged changes).
	cachedCmd := exec.Command("git", "diff", "--cached")
	cachedCmd.Dir = sess.Path
	staged, err := cachedCmd.Output()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to run git diff --cached: %v", err))
	}

	// Find untracked files and generate diffs for them.
	untrackedCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	untrackedCmd.Dir = sess.Path
	untrackedOut, err := untrackedCmd.Output()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to list untracked files: %v", err))
	}
	var untrackedDiff string
	if len(untrackedOut) > 0 {
		files := strings.Split(strings.TrimSpace(string(untrackedOut)), "\n")
		for _, f := range files {
			if f == "" {
				continue
			}
			// git diff --no-index exits with code 1 when there are differences,
			// so we ignore the error and just use the output.
			newCmd := exec.Command("git", "diff", "--no-index", "--", "/dev/null", f)
			newCmd.Dir = sess.Path
			out, _ := newCmd.CombinedOutput()
			untrackedDiff += string(out)
		}
	}

	// Combine output.
	combined := string(unstaged) + string(staged) + untrackedDiff

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"diff": combined})
}

func (h *Handler) gitLog(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	logCmd := exec.Command("git", "log", "--oneline", "--graph", "--decorate", "-30")
	logCmd.Dir = sess.Path
	out, err := logCmd.CombinedOutput()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to run git log: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"log": string(out)})
}

func (h *Handler) gitShow(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data struct {
		SessionID string `json:"session_id"`
		Hash      string `json:"hash"`
	}
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	if data.Hash == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "hash is required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	showCmd := exec.Command("git", "show", "--stat", "--patch", data.Hash)
	showCmd.Dir = sess.Path
	out, err := showCmd.CombinedOutput()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to run git show: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"show": string(out)})
}

func (h *Handler) gitInfo(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = sess.Path
	out, err := branchCmd.Output()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get branch: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"branch": strings.TrimSpace(string(out))})
}

func (h *Handler) listDirectory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ListDirectoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	path := data.Path
	if path == "" || path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		} else {
			path = "/"
		}
	} else if len(path) > 1 && path[0] == '~' && path[1] == '/' {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}

	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, fmt.Sprintf("cannot read directory: %v", err))
	}

	var entries []envelope.DirEntry
	for _, e := range dirEntries {
		// Skip hidden files/directories unless explicitly requested.
		if !data.ShowHidden && len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue
		}
		entries = append(entries, envelope.DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
		})
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.ListDirectoryResponse{
		Path:    path,
		Entries: entries,
	})
}

func (h *Handler) transferFile(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.TransferFileData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.Path == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "path is required")
	}

	// Expand ~ prefix.
	path := data.Path
	if len(path) > 1 && path[0] == '~' && path[1] == '/' {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, fmt.Sprintf("cannot stat file: %v", err))
	}
	if info.IsDir() {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidPath, "path is a directory, not a file")
	}

	// Limit file size to 50MB.
	const maxSize = 50 * 1024 * 1024
	if info.Size() > maxSize {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("file too large: %d bytes (max %d)", info.Size(), maxSize))
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("cannot read file: %v", err))
	}

	encoded := base64.StdEncoding.EncodeToString(content)

	return envelope.SuccessResponse(cmd.RequestID, envelope.TransferFileResponse{
		Path:    path,
		Name:    filepath.Base(path),
		Size:    info.Size(),
		Content: encoded,
	})
}

// attachmentMaxSize is the per-file cap for uploaded attachments (10MB raw).
const attachmentMaxSize = 10 * 1024 * 1024

// sanitizeAttachmentName reduces an arbitrary client-supplied filename to a safe
// basename with no path separators or traversal components. Returns "" if the
// result would be empty or unsafe.
func sanitizeAttachmentName(name string) string {
	// Strip any directory components the client may have included.
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.TrimSpace(name)
	// Reject traversal/edge cases.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return ""
	}
	return name
}

// saveAttachment writes a base64-encoded uploaded file into the session's
// attachments directory and returns its resolved absolute path. The agent later
// reads the file from this path (Copilot via --attachment, Claude via --add-dir).
func (h *Handler) saveAttachment(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SaveAttachmentData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" || data.Name == "" || data.Content == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id, name, and content are required")
	}

	// Verify session exists.
	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	name := sanitizeAttachmentName(data.Name)
	if name == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid attachment name")
	}

	content, err := base64.StdEncoding.DecodeString(data.Content)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid base64 content: %v", err))
	}
	if len(content) > attachmentMaxSize {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("attachment too large: %d bytes (max %d)", len(content), attachmentMaxSize))
	}

	dir := h.attachmentsDir(data.SessionID)
	if dir == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "no attachments directory available")
	}

	// Prefix with a short unique id to avoid collisions between same-named uploads.
	fname := newAgentSessionID() + "-" + name
	dest := filepath.Join(dir, fname)
	if err := os.WriteFile(dest, content, 0600); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("cannot write attachment: %v", err))
	}

	h.vlog("saved attachment %s (%d bytes) for session %s", dest, len(content), data.SessionID)

	return envelope.SuccessResponse(cmd.RequestID, envelope.SaveAttachmentResponse{
		Path: dest,
		Name: name,
		Size: int64(len(content)),
	})
}

// attachmentPromptAddendum builds a human-readable trailer listing attachment
// absolute paths, appended to the user prompt so the persisted turn reflects
// what was sent and (for Claude) so the agent knows where to find the files.
func attachmentPromptAddendum(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Attached files: ")
	for i, p := range paths {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p)
	}
	b.WriteString("]")
	return b.String()
}

func (h *Handler) schedule(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ScheduleData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" || data.Prompt == "" || data.ScheduledAt == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id, prompt, and scheduled_at are required")
	}

	// Verify session exists.
	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	scheduledAt, err := time.Parse(time.RFC3339, data.ScheduledAt)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid scheduled_at (expected RFC3339): %v", err))
	}

	// Validate cron expression if provided.
	if data.CronExpr != "" {
		if _, err := nextCronTime(data.CronExpr, time.Now()); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid cron expression: %v", err))
		}
	}

	id, err := h.store.CreateScheduleWithCron(data.SessionID, data.Prompt, scheduledAt, data.CronExpr)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to create schedule: %v", err))
	}

	h.vlog("created schedule %d for session %s at %s", id, data.SessionID, scheduledAt.Format(time.RFC3339))

	// Notify about new schedule.
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventChatSchedule, data.SessionID, map[string]interface{}{
			"schedule_id": id,
			"prompt":      data.Prompt,
			"schedule_at": scheduledAt.UTC().Format(time.RFC3339),
			"action":      "created",
		})
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.ScheduleResponse{
		ScheduleID:  id,
		SessionID:   data.SessionID,
		ScheduledAt: scheduledAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) listSchedules(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ListSchedulesData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	schedules, err := h.store.ListSchedules(data.SessionID, "")
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to list schedules: %v", err))
	}

	var infos []envelope.ScheduleInfo
	for _, s := range schedules {
		infos = append(infos, envelope.ScheduleInfo{
			ID:          s.ID,
			SessionID:   s.SessionID,
			Prompt:      s.Prompt,
			ScheduledAt: s.ScheduledAt.UTC().Format(time.RFC3339),
			Status:      s.Status,
			CronExpr:    s.CronExpr,
			CreatedAt:   s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.ListSchedulesResponse{Schedules: infos})
}

func (h *Handler) cancelSchedule(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.CancelScheduleData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}

	if data.ScheduleID == 0 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "schedule_id is required")
	}

	// Get schedule details before canceling for notification.
	schedule, err := h.store.GetSchedule(data.ScheduleID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get schedule: %v", err))
	}

	if err := h.store.CancelSchedule(data.ScheduleID); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to cancel schedule: %v", err))
	}

	h.vlog("cancelled schedule %d", data.ScheduleID)

	// Notify about cancelled schedule.
	if h.notifyWriter != nil && schedule != nil {
		_ = h.notifyWriter.Send(envelope.EventChatSchedule, schedule.SessionID, map[string]interface{}{
			"schedule_id": data.ScheduleID,
			"action":      "deleted",
		})
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]interface{}{"schedule_id": data.ScheduleID, "cancelled": true})
}

// scheduleTagRe matches <schedule at="...">...</schedule> tags in agent output.
var scheduleTagRe = regexp.MustCompile(`<schedule\s+at="([^"]+)">([\s\S]*?)</schedule>`)

// parseAndCreateSchedules extracts <schedule> tags from agent output, creates schedule entries,
// and returns the cleaned output with tags replaced by human-readable notes.
func (h *Handler) parseAndCreateSchedules(sessionID, output string) string {
	return scheduleTagRe.ReplaceAllStringFunc(output, func(match string) string {
		sub := scheduleTagRe.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		atStr := sub[1]
		prompt := strings.TrimSpace(sub[2])

		scheduledAt, err := parseScheduleTime(atStr)
		if err != nil {
			h.vlog("invalid schedule time %q in agent output for session %s: %v", atStr, sessionID, err)
			return match
		}

		id, err := h.store.CreateSchedule(sessionID, prompt, scheduledAt)
		if err != nil {
			h.vlog("failed to create schedule from agent output for session %s: %v", sessionID, err)
			return match
		}

		h.vlog("agent self-scheduled %d for session %s at %s", id, sessionID, scheduledAt.Format(time.RFC3339))
		return fmt.Sprintf("\n[Scheduled follow-up for %s]\n", scheduledAt.Local().Format("Jan 2, 3:04 PM"))
	})
}

// parseScheduleTime parses a time string that can be RFC3339 or a relative duration like "+2h", "+30m".
func parseScheduleTime(s string) (time.Time, error) {
	// Try RFC3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}

	// Try relative time: +Nh, +Nm, +Ns, or combinations.
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "+") {
		d, err := parseRelativeDuration(s[1:])
		if err == nil {
			return time.Now().UTC().Add(d), nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

// parseRelativeDuration parses duration strings like "2h", "30m", "2h30m", "1h30m15s".
func parseRelativeDuration(s string) (time.Duration, error) {
	// Go's time.ParseDuration handles "2h30m" etc.
	return time.ParseDuration(s)
}

// StartScheduler starts the background scheduler that checks for due schedules.
// It runs an immediate check, then ticks every 30 seconds.
// Cancel the context to stop the scheduler.
func (h *Handler) StartScheduler(ctx context.Context) {
	h.processDueSchedules()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.processDueSchedules()
			}
		}
	}()
}

func (h *Handler) processDueSchedules() {
	schedules, err := h.store.DueSchedules()
	if err != nil {
		h.vlog("scheduler: failed to query due schedules: %v", err)
		return
	}

	for _, sch := range schedules {
		h.vlog("scheduler: processing schedule %d for session %s", sch.ID, sch.SessionID)

		// Mark as running.
		if err := h.store.UpdateScheduleStatus(sch.ID, store.ScheduleRunning); err != nil {
			h.vlog("scheduler: failed to update schedule %d status: %v", sch.ID, err)
			continue
		}

		// Notify about schedule execution.
		if h.notifyWriter != nil {
			_ = h.notifyWriter.Send(envelope.EventChatSchedule, sch.SessionID, map[string]interface{}{
				"schedule_id": sch.ID,
				"prompt":      sch.Prompt,
				"schedule_at": sch.ScheduledAt.Format(time.RFC3339),
				"action":      "executed",
			})
		}

		sess, err := h.store.GetSession(sch.SessionID)
		if err != nil || sess == nil {
			h.vlog("scheduler: session %s not found for schedule %d", sch.SessionID, sch.ID)
			_ = h.store.UpdateScheduleStatus(sch.ID, store.ScheduleDone)
			continue
		}

		// Add a system notification turn so the user sees the scheduled prompt in chat.
		label := "Scheduled task"
		if sch.CronExpr != "" {
			label = fmt.Sprintf("Recurring task (%s)", sch.CronExpr)
		}
		schedNotice := fmt.Sprintf("[%s triggered at %s]", label, time.Now().Local().Format("Jan 2, 3:04 PM"))
		_ = h.store.AddConversationTurn(sch.SessionID, "system", schedNotice)

		if sess.Status == store.StateIdle {
			// Session is idle — continue it directly.
			if err := h.store.UpdateSessionStatus(sch.SessionID, store.StateWorking); err != nil {
				h.vlog("scheduler: failed to set session %s to working: %v", sch.SessionID, err)
				_ = h.store.UpdateScheduleStatus(sch.ID, store.SchedulePending)
				continue
			}
			if err := h.store.AddConversationTurn(sch.SessionID, "scheduled", sch.Prompt); err != nil {
				h.vlog("scheduler: failed to add conversation turn for session %s: %v", sch.SessionID, err)
			}
			_ = h.store.UpdateScheduleStatus(sch.ID, store.ScheduleDone)

			// If this is a recurring schedule, create the next occurrence.
			if sch.CronExpr != "" {
				h.scheduleNextCron(sch)
			}

			go h.runAgent(sch.SessionID, agent.RunParams{
				SessionID:    sch.SessionID,
				Agent:        sess.Agent,
				Prompt:       sch.Prompt,
				SystemPrompt: sess.SystemPrompt,
				Model:        sess.Model,
				Effort:       sess.Effort,
				Yolo:         sess.Yolo,
				Path:         sess.Path,
				Resume:       true,
			})
		} else {
			// Session is busy — queue the prompt, it'll run after current task finishes.
			if err := h.store.QueuePrompt(sch.SessionID, sch.Prompt, "", "", "scheduled"); err != nil {
				h.vlog("scheduler: failed to queue prompt for session %s: %v", sch.SessionID, err)
				_ = h.store.UpdateScheduleStatus(sch.ID, store.SchedulePending)
				continue
			}
			_ = h.store.UpdateScheduleStatus(sch.ID, store.ScheduleDone)

			// If this is a recurring schedule, create the next occurrence.
			if sch.CronExpr != "" {
				h.scheduleNextCron(sch)
			}

			h.vlog("scheduler: session %s busy, queued scheduled prompt (schedule %d)", sch.SessionID, sch.ID)
		}
	}
}

// scheduleNextCron creates the next occurrence of a recurring schedule.
func (h *Handler) scheduleNextCron(sch *store.Schedule) {
	next, err := nextCronTime(sch.CronExpr, time.Now())
	if err != nil {
		h.vlog("scheduler: invalid cron expression %q for schedule %d: %v", sch.CronExpr, sch.ID, err)
		return
	}
	id, err := h.store.CreateScheduleWithCron(sch.SessionID, sch.Prompt, next, sch.CronExpr)
	if err != nil {
		h.vlog("scheduler: failed to create next cron occurrence for schedule %d: %v", sch.ID, err)
		return
	}
	h.vlog("scheduler: created next cron occurrence %d for session %s at %s", id, sch.SessionID, next.Format(time.RFC3339))
}

// nextCronTime computes the next occurrence after `after` for a cron expression.
// Supports standard 5-field cron: minute hour day-of-month month day-of-week.
// Also supports simple interval shorthands: @every 1h, @every 30m, @daily, @hourly.
func nextCronTime(expr string, after time.Time) (time.Time, error) {
	expr = strings.TrimSpace(expr)

	// Handle shorthands.
	switch {
	case expr == "@hourly":
		return after.Truncate(time.Hour).Add(time.Hour), nil
	case expr == "@daily":
		next := time.Date(after.Year(), after.Month(), after.Day()+1, 0, 0, 0, 0, after.Location())
		return next, nil
	case strings.HasPrefix(expr, "@every "):
		durStr := strings.TrimPrefix(expr, "@every ")
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid interval: %w", err)
		}
		return after.Add(d), nil
	}

	// Parse standard 5-field cron: min hour dom month dow
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, fmt.Errorf("expected 5 fields in cron expression, got %d", len(fields))
	}

	// Simple cron parser: supports numbers and * only (no ranges/steps for now).
	parseField := func(s string, min, max int) ([]int, error) {
		if s == "*" {
			vals := make([]int, max-min+1)
			for i := range vals {
				vals[i] = min + i
			}
			return vals, nil
		}
		var val int
		if _, err := fmt.Sscanf(s, "%d", &val); err != nil {
			return nil, fmt.Errorf("invalid cron field %q", s)
		}
		if val < min || val > max {
			return nil, fmt.Errorf("cron field %d out of range [%d-%d]", val, min, max)
		}
		return []int{val}, nil
	}

	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, err
	}
	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, err
	}
	doms, err := parseField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, err
	}
	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, err
	}
	dows, err := parseField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, err
	}

	domSet := make(map[int]bool)
	for _, v := range doms {
		domSet[v] = true
	}
	monSet := make(map[int]bool)
	for _, v := range months {
		monSet[v] = true
	}
	dowSet := make(map[int]bool)
	for _, v := range dows {
		dowSet[v] = true
	}

	// Iterate minute by minute from after+1min, up to 1 year.
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.Add(366 * 24 * time.Hour)
	for t.Before(limit) {
		if monSet[int(t.Month())] && domSet[t.Day()] && dowSet[int(t.Weekday())] {
			for _, h := range hours {
				for _, m := range minutes {
					candidate := time.Date(t.Year(), t.Month(), t.Day(), h, m, 0, 0, t.Location())
					if candidate.After(after) {
						return candidate, nil
					}
				}
			}
		}
		t = t.Add(24 * time.Hour)
		t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	}

	return time.Time{}, fmt.Errorf("no matching time found within 1 year for cron %q", expr)
}

// Git operation handlers.

func (h *Handler) gitCommit(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.GitCommitData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	// A no-edit amend reuses the previous commit message, so no message is
	// required; every other commit (including a message-changing amend) needs one.
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	if data.Message == "" && !(data.Amend && data.NoEdit) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "message is required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Stage changes. When specific files are given, stage only those so the
	// commit is restricted to the reviewed paths; otherwise stage everything.
	var addArgs []string
	if len(data.Files) > 0 {
		addArgs = append([]string{"add", "--"}, data.Files...)
	} else {
		addArgs = []string{"add", "-A"}
	}
	addCmd := exec.Command("git", addArgs...)
	addCmd.Dir = sess.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("git add failed: %s", string(out)))
	}

	// Commit (or amend).
	var commitArgs []string
	if data.Amend {
		if data.NoEdit {
			commitArgs = []string{"commit", "--amend", "--no-edit"}
		} else {
			commitArgs = []string{"commit", "--amend", "-m", data.Message}
		}
	} else {
		commitArgs = []string{"commit", "-m", data.Message}
	}
	// Restrict the commit to the given files (partial commit) so unrelated
	// already-staged changes are not included.
	if len(data.Files) > 0 {
		commitArgs = append(commitArgs, "--")
		commitArgs = append(commitArgs, data.Files...)
	}
	commitCmd := exec.Command("git", commitArgs...)
	commitCmd.Dir = sess.Path
	out, err := commitCmd.CombinedOutput()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("git commit failed: %s", string(out)))
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.GitResponse{Output: string(out)})
}

func (h *Handler) gitBranch(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.GitBranchData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" || data.BranchName == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id and branch_name are required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Create and switch to new branch.
	branchCmd := exec.Command("git", "checkout", "-b", data.BranchName)
	branchCmd.Dir = sess.Path
	out, err := branchCmd.CombinedOutput()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("git checkout -b failed: %s", string(out)))
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.GitResponse{Output: string(out)})
}

func (h *Handler) gitPush(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.GitPushData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Get current branch name.
	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = sess.Path
	branchOut, err := branchCmd.Output()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get branch name: %v", err))
	}
	branch := strings.TrimSpace(string(branchOut))

	// Push with -u to set upstream.
	pushArgs := []string{"push", "-u", "origin", branch}
	if data.Force {
		pushArgs = []string{"push", "--force-with-lease", "-u", "origin", branch}
	}
	pushCmd := exec.Command("git", pushArgs...)
	pushCmd.Dir = sess.Path
	out, err := pushCmd.CombinedOutput()
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("git push failed: %s", string(out)))
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.GitResponse{Output: string(out)})
}
