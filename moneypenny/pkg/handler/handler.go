package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

// Handler processes commands and returns responses.
type Handler struct {
	store   *store.Store
	runner  *agent.Runner
	version string
	vlog    func(string, ...interface{})
}

// resultCallback is called when an async agent execution completes.
// It can be set to push notifications, but by default is nil.
type resultCallback func(sessionID, response string, err error)

// New creates a new Handler with the given store, runner, and version string.
func New(s *store.Store, runner *agent.Runner, version string) *Handler {
	return &Handler{store: s, runner: runner, version: version, vlog: func(string, ...interface{}) {}}
}

// SetLogger sets a verbose logger.
func (h *Handler) SetLogger(vlog func(string, ...interface{})) {
	h.vlog = vlog
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
	case "delete_session":
		return h.deleteSession(ctx, cmd)
	case "stop_session":
		return h.stopSession(ctx, cmd)
	case "update_session":
		return h.updateSession(ctx, cmd)
	case "get_version":
		return h.getVersion(cmd)
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

	// Set status to working.
	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateWorking); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
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
		SystemPrompt: data.SystemPrompt,
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
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Resume:       true,
	})

	return envelope.SuccessResponse(cmd.RequestID, envelope.ContinueSessionResponse{
		SessionID: data.SessionID,
	})
}

// runAgent executes the agent in the background, updating the store when done.
func (h *Handler) runAgent(sessionID string, params agent.RunParams) {
	ctx := context.Background()
	result, err := h.runner.Run(ctx, params)
	if err != nil {
		h.vlog("agent error for session %s: %v", sessionID, err)
		_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)
		return
	}

	if err := h.store.AddConversationTurn(sessionID, "assistant", result.Text); err != nil {
		h.vlog("failed to add conversation turn for session %s: %v", sessionID, err)
	}

	if err := h.store.UpdateSessionStatus(sessionID, store.StateIdle); err != nil {
		h.vlog("failed to update status for session %s: %v", sessionID, err)
	}

	h.vlog("agent completed for session %s", sessionID)
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
		}
		if ts, err := h.store.GetSessionTimestamps(s.SessionID); err == nil && ts != nil {
			info.CreatedAt = ts.FirstTurn.UTC().Format("2006-01-02T15:04:05Z")
			info.LastAccessed = ts.LastTurn.UTC().Format("2006-01-02T15:04:05Z")
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

	turns, err := h.store.GetConversation(data.SessionID)
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

	detail := envelope.SessionDetail{
		SessionID:    sess.SessionID,
		Name:         sess.Name,
		Status:       sess.Status,
		Agent:        sess.Agent,
		SystemPrompt: sess.SystemPrompt,
		Yolo:         sess.Yolo,
		Path:         sess.Path,
		Conversation: conversation,
	}

	return envelope.SuccessResponse(cmd.RequestID, detail)
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

	if err := h.store.UpdateSessionFields(data.SessionID, data.Name, data.SystemPrompt, data.Path, data.Yolo); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update session: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}

func (h *Handler) getVersion(cmd *envelope.Command) *envelope.Response {
	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"version": h.version})
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

	if sess.Status != store.StateWorking {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotWorking, fmt.Sprintf("session is not working: %s", sess.Status))
	}

	if err := h.runner.Stop(data.SessionID); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to stop session: %v", err))
	}

	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateIdle); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}

	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"session_id": data.SessionID})
}
