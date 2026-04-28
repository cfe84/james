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
	store            *store.Store
	runner           *agent.Runner
	version          string
	vlog             func(string, ...interface{})
	updateStatusFunc func() envelope.UpdateStatusResponse
	notifyWriter     *envelope.NotificationWriter // for sending async notifications to hem
}

// resultCallback is called when an async agent execution completes.
// It can be set to push notifications, but by default is nil.
type resultCallback func(sessionID, response string, err error)

// New creates a new Handler with the given store, runner, and version string.
func New(s *store.Store, runner *agent.Runner, version string) *Handler {
	h := &Handler{store: s, runner: runner, version: version, vlog: func(string, ...interface{}) {}}
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

// SetLogger sets a verbose logger.
func (h *Handler) SetLogger(vlog func(string, ...interface{})) {
	h.vlog = vlog
}

// SetUpdateStatusFunc sets the function called to get update status from the updater.
func (h *Handler) SetUpdateStatusFunc(f func() envelope.UpdateStatusResponse) {
	h.updateStatusFunc = f
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
	case "schedule":
		return h.schedule(ctx, cmd)
	case "list_schedules":
		return h.listSchedules(ctx, cmd)
	case "cancel_schedule":
		return h.cancelSchedule(ctx, cmd)
	case "get_memory":
		return h.getMemory(ctx, cmd)
	case "update_memory":
		return h.updateMemory(ctx, cmd)
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

	// Check agent binary exists.
	if _, err := exec.LookPath(data.Agent); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrAgentNotFound, fmt.Sprintf("agent binary not found: %s", data.Agent))
	}

	systemPrompt := data.SystemPrompt

	// Create session in store.
	sess := &store.Session{
		SessionID:    data.SessionID,
		Name:         data.Name,
		Agent:        data.Agent,
		SystemPrompt: systemPrompt,
		Model:        data.Model,
		Effort:       data.Effort,
		Yolo:         data.Yolo,
		Path:         data.Path,
	}
	if err := h.store.CreateSession(sess); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionAlreadyExists, fmt.Sprintf("session already exists: %s", data.SessionID))
	}

	// Set status to working.
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

	// Add user prompt to conversation.
	if err := h.store.AddConversationTurn(data.SessionID, "user", data.Prompt); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to add conversation turn: %v", err))
	}

	// Run agent asynchronously with Resume=true.
	go h.runAgent(data.SessionID, agent.RunParams{
		SessionID:    data.SessionID,
		Agent:        sess.Agent,
		Prompt:       data.Prompt,
		SystemPrompt: sess.SystemPrompt,
		Model:        sess.Model,
		Effort:       sess.Effort,
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Resume:       true,
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

	if err := h.store.QueuePrompt(data.SessionID, data.Prompt); err != nil {
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

// runAgent executes the agent in the background, updating the store when done.
// After completion, it checks the prompt queue and auto-continues if there are queued prompts.
func (h *Handler) runAgent(sessionID string, params agent.RunParams) {
	// Inject session memory into system prompt at runtime so agents always
	// see the latest memory content, even if it was updated since session creation.
	if memory, err := h.store.GetMemory(sessionID); err == nil && memory != "" {
		params.SystemPrompt += "\n\n<session-memory>\n" + memory + "\n</session-memory>"
	}

	ctx := context.Background()
	result, err := h.runner.Run(ctx, params)
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

	if err := h.store.AddConversationTurn(sessionID, "assistant", responseText); err != nil {
		h.vlog("failed to add conversation turn for session %s: %v", sessionID, err)
	}

	h.vlog("agent completed for session %s", sessionID)

	// Check for queued prompts before going idle.
	prompts, err := h.store.DrainQueue(sessionID)
	if err != nil {
		h.vlog("failed to drain queue for session %s: %v", sessionID, err)
	}

	if len(prompts) > 0 {
		h.vlog("processing %d queued prompt(s) for session %s", len(prompts), sessionID)

		// Re-fetch session for latest settings.
		sess, err := h.store.GetSession(sessionID)
		if err != nil || sess == nil {
			h.vlog("failed to get session %s for queued continuation: %v", sessionID, err)
			_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)
			return
		}

		// Process each queued prompt as its own turn.
		for _, prompt := range prompts {
			if err := h.store.AddConversationTurn(sessionID, "user", prompt); err != nil {
				h.vlog("failed to add queued conversation turn for session %s: %v", sessionID, err)
			}
		}

		// Continue with all queued prompts joined for the agent.
		combinedPrompt := strings.Join(prompts, "\n")
		h.runAgent(sessionID, agent.RunParams{
			SessionID:    sessionID,
			Agent:        sess.Agent,
			Prompt:       combinedPrompt,
			SystemPrompt: sess.SystemPrompt,
			Model:        sess.Model,
			Effort:       sess.Effort,
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
		SessionID:    sess.SessionID,
		Name:         sess.Name,
		Status:       sess.Status,
		Agent:        sess.Agent,
		SystemPrompt: sess.SystemPrompt,
		Model:        sess.Model,
		Effort:       sess.Effort,
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Memory:       sess.Memory,
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

	if err := h.store.UpdateSessionFields(data.SessionID, data.Name, data.SystemPrompt, data.Model, data.Effort, data.Path, data.Yolo); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update session: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}

func (h *Handler) getMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	content, err := h.store.GetMemory(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, err.Error())
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.MemoryResponse{
		SessionID: data.SessionID,
		Content:   content,
	})
}

func (h *Handler) updateMemory(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.UpdateMemoryData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}

	if err := h.store.SetMemory(data.SessionID, data.Content); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}

	return envelope.SuccessResponse(cmd.RequestID, envelope.MemoryResponse{
		SessionID: data.SessionID,
		Content:   data.Content,
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
	copilotModelCacheTTL  = 1 * time.Hour
)

// copilotModels queries copilot for available models by running a prompt.
// Results are cached for 1 hour to avoid repeated slow queries.
func copilotModels() []envelope.ModelInfo {
	if len(copilotModelCache) > 0 && time.Since(copilotModelCacheTime) < copilotModelCacheTTL {
		log.Printf("copilot models: returning %d cached models (age: %v)", len(copilotModelCache), time.Since(copilotModelCacheTime))
		return copilotModelCache
	}
	path, err := exec.LookPath("copilot")
	if err != nil {
		log.Printf("copilot models: copilot not found in PATH: %v", err)
		return nil
	}

	log.Printf("copilot models: querying copilot at %s", path)

	// Ask copilot to list its available models. Use --available-tools '' to
	// prevent it from using tools (faster, cheaper). Timeout after 30 seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path,
		"-p", "List the model identifiers available for --model. One per line. No other text, no markdown formatting.",
		"--output-format", "text",
		"--available-tools", "",
	)
	out, err := cmd.Output()
	if err != nil {
		log.Printf("copilot models: query failed: %v", err)
		return nil
	}

	log.Printf("copilot models: raw output (%d bytes): %q", len(out), string(out))

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
		log.Printf("copilot models: cached %d models", len(models))
		return models
	}
	log.Printf("copilot models: no models parsed from output")
	return nil
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
			if err := h.store.AddConversationTurn(sch.SessionID, "user", sch.Prompt); err != nil {
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
			if err := h.store.QueuePrompt(sch.SessionID, sch.Prompt); err != nil {
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
	if data.SessionID == "" || data.Message == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id and message are required")
	}

	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}

	// Stage all changes.
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = sess.Path
	if out, err := addCmd.CombinedOutput(); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("git add failed: %s", string(out)))
	}

	// Commit (or amend).
	var commitArgs []string
	if data.Amend {
		commitArgs = []string{"commit", "--amend", "-m", data.Message}
	} else {
		commitArgs = []string{"commit", "-m", data.Message}
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
