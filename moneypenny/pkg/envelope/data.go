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
	// CompactionMode selects how context is compacted: "agent" (rely on the
	// underlying agent's built-in compaction) or "custom" (James-managed
	// distillation + summary + fresh-session substitution). Empty defaults to
	// "custom" for new sessions.
	CompactionMode string `json:"compaction_mode,omitempty"`
	// CopyMemoryFrom, when set, names a source session on this same moneypenny
	// whose memory folder should be copied into the new session's memory folder
	// (used when duplicating a session so the copy inherits accumulated memory).
	CopyMemoryFrom string `json:"copy_memory_from,omitempty"`
}

// ContinueSessionData is the data payload for continue_session.
// Model and Effort are optional per-prompt overrides (empty = use the
// session's stored default).
type ContinueSessionData struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// UpdateSessionData is the data payload for update_session.
// Only non-nil pointer fields are updated.
type UpdateSessionData struct {
	SessionID      string  `json:"session_id"`
	Name           *string `json:"name,omitempty"`
	SystemPrompt   *string `json:"system_prompt,omitempty"`
	Model          *string `json:"model,omitempty"`
	Effort         *string `json:"effort,omitempty"`
	Yolo           *bool   `json:"yolo,omitempty"`
	Path           *string `json:"path,omitempty"`
	CompactionMode *string `json:"compaction_mode,omitempty"`
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
	Agent        string `json:"agent,omitempty"`
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
	// CompactionMode is "agent" or "custom".
	CompactionMode string `json:"compaction_mode,omitempty"`
	// ContextTokens is the last-measured (Claude) or estimated (Copilot) size
	// of the underlying agent's context, and ContextWindow is the model's max.
	// Both are 0 when never measured. Surfaced so clients can show usage and so
	// the burned-in window table can be tuned by observation.
	ContextTokens int `json:"context_tokens,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`
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

// SummarizeSessionData is the data payload for summarize_session.
// Asks the moneypenny to compact the session's conversation history into a
// standalone summary using the session's configured agent.
type SummarizeSessionData struct {
	SessionID string `json:"session_id"`
}

// SummarizeSessionResponse is returned by summarize_session.
// TurnCount is the number of stored conversation turns the summarizer saw. It
// lets callers distinguish "no history yet" (TurnCount==0) from "history exists
// but the agent returned an empty summary" (TurnCount>0, Summary==""), so the
// copy/summarize paths don't silently fabricate a "no history" preamble.
type SummarizeSessionResponse struct {
	SessionID string `json:"session_id"`
	Summary   string `json:"summary"`
	TurnCount int    `json:"turn_count"`
}

// CompactSessionData is the data payload for compact_session. Runs the full
// custom-compaction pipeline (in-session distillation into memory + handoff
// summary + fresh underlying-agent substitution) regardless of the session's
// configured compaction mode.
type CompactSessionData struct {
	SessionID string `json:"session_id"`
}

// CompactSessionResponse is returned by compact_session once the pipeline has
// been kicked off (the work itself runs asynchronously).
type CompactSessionResponse struct {
	SessionID string `json:"session_id"`
}

// DistillSessionData is the data payload for distill_session. Asks the
// moneypenny to run the session's agent (same agent/model/effort, but a fresh
// throwaway underlying agent session — the live one is left untouched) over the
// full transcript, instructing it to inspect existing hierarchical memory and
// extract everything important from the conversation into it.
type DistillSessionData struct {
	SessionID string `json:"session_id"`
}

// DistillSessionResponse is returned by distill_session once the distillation
// has been kicked off (the work itself runs asynchronously).
type DistillSessionResponse struct {
	SessionID string `json:"session_id"`
}

// MemoryNodePayload is a single hierarchical memory node in protocol responses.
type MemoryNodePayload struct {
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Body        string `json:"body,omitempty"`
}

// ShowMemoryData is the data payload for show_memory. Path is optional: empty
// path requests the full outline.
type ShowMemoryData struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path,omitempty"`
}

// ShowMemoryResponse is returned by show_memory. When Path is empty, Outline
// holds the body-less tree outline and Nodes holds the full flat node list
// (path/title/description, DFS pre-order) for clients that render a tree. When
// Path is set, Node holds that node and Children lists its immediate children
// (path/title/description only).
type ShowMemoryResponse struct {
	SessionID string              `json:"session_id"`
	Path      string              `json:"path,omitempty"`
	Outline   string              `json:"outline,omitempty"`
	Nodes     []MemoryNodePayload `json:"nodes,omitempty"`
	Node      *MemoryNodePayload  `json:"node,omitempty"`
	Children  []MemoryNodePayload `json:"children,omitempty"`
}

// ListMemoryData is the data payload for list_memory.
type ListMemoryData struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path,omitempty"`
}

// ListMemoryResponse is returned by list_memory: immediate children under Path.
type ListMemoryResponse struct {
	SessionID string              `json:"session_id"`
	Path      string              `json:"path,omitempty"`
	Children  []MemoryNodePayload `json:"children"`
}

// SearchMemoryData is the data payload for search_memory.
type SearchMemoryData struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
}

// SearchMemoryResponse is returned by search_memory: ranked matching nodes.
type SearchMemoryResponse struct {
	SessionID string              `json:"session_id"`
	Query     string              `json:"query"`
	Results   []MemoryNodePayload `json:"results"`
}

// UpdateMemoryData is the data payload for update_memory: create/replace one
// node at Path.
type UpdateMemoryData struct {
	SessionID   string `json:"session_id"`
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Body        string `json:"body"`
}

// DeleteMemoryData is the data payload for delete_memory.
type DeleteMemoryData struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

// MemoryWriteResponse is returned by update_memory and delete_memory.
type MemoryWriteResponse struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
	Deleted   int    `json:"deleted,omitempty"`
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
