package envelope

// Notification events
const (
	EventSessionStateChanged = "session_state_changed"
	EventSessionCompleted    = "session_completed"
	EventSessionError        = "session_error"
	EventChatActivity        = "chat_activity"
	EventChatMessage         = "chat_message"
	EventChatStatus          = "chat_status"
	EventChatSubagent        = "chat_subagent"
	EventChatSchedule        = "chat_schedule"
)

// CreateSessionData is the data payload for create_session.
type CreateSessionData struct {
	Agent        string `json:"agent"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Yolo         bool   `json:"yolo"`
	Prompt       string `json:"prompt"`
	SessionID    string `json:"session_id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
}

// ContinueSessionData is the data payload for continue_session.
type ContinueSessionData struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

// UpdateSessionData is the data payload for update_session.
// Only non-nil pointer fields are updated.
type UpdateSessionData struct {
	SessionID    string  `json:"session_id"`
	Name         *string `json:"name,omitempty"`
	SystemPrompt *string `json:"system_prompt,omitempty"`
	Model        *string `json:"model,omitempty"`
	Effort       *string `json:"effort,omitempty"`
	Yolo         *bool   `json:"yolo,omitempty"`
	Path         *string `json:"path,omitempty"`
}

// ImportSessionData is the data payload for import_session.
// Creates a session with conversation history without running an agent.
type ImportSessionData struct {
	SessionID    string             `json:"session_id"`
	Name         string             `json:"name"`
	Agent        string             `json:"agent"`
	SystemPrompt string             `json:"system_prompt,omitempty"`
	Yolo         bool               `json:"yolo,omitempty"`
	Path         string             `json:"path"`
	Conversation []ConversationTurn `json:"conversation"`
}

// SessionIDData is used by methods that only need a session_id (get_session, delete_session, stop_session).
type SessionIDData struct {
	SessionID string `json:"session_id"`
}

// GetConversationData is the data payload for get_session_conversation.
type GetConversationData struct {
	SessionID string `json:"session_id"`
	Count     int    `json:"count,omitempty"` // number of turns to return (default 10, 0 = use default)
	From      int    `json:"from,omitempty"`  // offset from the end (0 = most recent)
	All       bool   `json:"all,omitempty"`   // return all turns
}

// SessionInfo is returned by list_sessions for each session.
type SessionInfo struct {
	SessionID    string `json:"session_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at,omitempty"`
	LastAccessed string `json:"last_accessed,omitempty"`
}

// SessionDetail is returned by get_session (metadata only, no conversation).
type SessionDetail struct {
	SessionID    string `json:"session_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Agent        string `json:"agent"`
	SystemPrompt string `json:"system_prompt"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	Yolo         bool   `json:"yolo"`
	Path         string `json:"path"`
	Memory       string `json:"memory,omitempty"`
	LastAccessed string `json:"last_accessed,omitempty"`
}

// SessionConversation is returned by get_session_conversation.
type SessionConversation struct {
	SessionID    string             `json:"session_id"`
	Conversation []ConversationTurn `json:"conversation"`
	Total        int                `json:"total"` // total number of turns in the session
}

// ConversationTurn represents a single prompt/response pair.
type ConversationTurn struct {
	Role      string `json:"role"`    // "user" or "assistant"
	Content   string `json:"content"`
	CreatedAt string `json:"created_at,omitempty"`
}

// CreateSessionResponse is returned by create_session on success.
type CreateSessionResponse struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
}

// ContinueSessionResponse is returned by continue_session on success.
type ContinueSessionResponse struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
}

// UpdateMemoryData is the data payload for update_memory.
type UpdateMemoryData struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

// MemoryResponse is returned by get_memory and update_memory.
type MemoryResponse struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

// ListDirectoryData is the data payload for list_directory.
type ListDirectoryData struct {
	Path       string `json:"path"`
	ShowHidden bool   `json:"show_hidden,omitempty"`
}

// DirEntry represents a single directory entry.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

// ListDirectoryResponse is returned by list_directory.
type ListDirectoryResponse struct {
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
}

// TransferFileData is the data payload for transfer_file.
type TransferFileData struct {
	Path string `json:"path"`
}

// TransferFileResponse is returned by transfer_file.
type TransferFileResponse struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Content string `json:"content"` // base64-encoded file content
}

// ScheduleData is the data payload for schedule.
type ScheduleData struct {
	SessionID   string `json:"session_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"` // RFC3339 UTC
	CronExpr    string `json:"cron_expr,omitempty"`
}

// ScheduleResponse is returned by schedule on success.
type ScheduleResponse struct {
	ScheduleID  int64  `json:"schedule_id"`
	SessionID   string `json:"session_id"`
	ScheduledAt string `json:"scheduled_at"`
}

// ListSchedulesData is the data payload for list_schedules.
type ListSchedulesData struct {
	SessionID string `json:"session_id"`
}

// ScheduleInfo represents a schedule in list responses.
type ScheduleInfo struct {
	ID          int64  `json:"id"`
	SessionID   string `json:"session_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"`
	Status      string `json:"status"`
	CronExpr    string `json:"cron_expr,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// ListSchedulesResponse is returned by list_schedules.
type ListSchedulesResponse struct {
	Schedules []ScheduleInfo `json:"schedules"`
}

// CancelScheduleData is the data payload for cancel_schedule.
type CancelScheduleData struct {
	ScheduleID int64 `json:"schedule_id"`
}

// GitCommitData is the data payload for git_commit.
type GitCommitData struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Amend     bool   `json:"amend,omitempty"`
}

// GitBranchData is the data payload for git_branch.
type GitBranchData struct {
	SessionID  string `json:"session_id"`
	BranchName string `json:"branch_name"`
}

// GitPushData is the data payload for git_push.
type GitPushData struct {
	SessionID string `json:"session_id"`
	Force     bool   `json:"force,omitempty"`
}

// GitResponse is returned by git operations on success.
type GitResponse struct {
	Output string `json:"output"`
}

// ExecuteCommandData is the data payload for execute_command.
type ExecuteCommandData struct {
	Command string `json:"command"`
	Path    string `json:"path"`
}

// ExecuteCommandResponse is returned by execute_command on success.
type ExecuteCommandResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// ListModelsData is the data payload for list_models.
type ListModelsData struct {
	Agent string `json:"agent"`
}

// ModelInfo describes an available model.
type ModelInfo struct {
	Name  string `json:"name"`            // display name or alias (e.g. "sonnet")
	Value string `json:"value,omitempty"` // full model ID if different from name
}

// ListModelsResponse is returned by list_models.
type ListModelsResponse struct {
	Agent  string      `json:"agent"`
	Models []ModelInfo `json:"models"`
}

// ActivityEvent represents a single agent activity step (thinking, tool use, etc.).
type ActivityEvent struct {
	Type      string `json:"type"`      // "thinking", "tool_use", "text"
	Summary   string `json:"summary"`   // short description
	Timestamp string `json:"timestamp"` // RFC3339
}

// SessionActivityResponse is returned by get_session_activity.
type SessionActivityResponse struct {
	SessionID string          `json:"session_id"`
	Activity  []ActivityEvent `json:"activity"`
}

// CheckAgentsResponse is returned by check_agents.
type CheckAgentsResponse struct {
	Agents []AgentAvailability `json:"agents"`
}

// AgentAvailability describes whether an agent binary is available.
type AgentAvailability struct {
	Name  string `json:"name"`
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
}

// UpdateStatusResponse is returned by update_status.
type UpdateStatusResponse struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Status          string `json:"status"`
	LastChecked     string `json:"last_checked,omitempty"`
	Error           string `json:"error,omitempty"`
}

// CheckUpdateResponse is returned by check_update.
type CheckUpdateResponse struct {
	Queued bool `json:"queued"` // true if a check was queued, false if already pending
}
