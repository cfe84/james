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
	Agent        string            `json:"agent"`
	SystemPrompt string            `json:"system_prompt"`
	Model        string            `json:"model,omitempty"`
	Effort       string            `json:"effort,omitempty"`
	ContextTier  string            `json:"context_tier,omitempty"`
	Yolo         bool              `json:"yolo"`
	Prompt       string            `json:"prompt"`
	SessionID    string            `json:"session_id"`
	Name         string            `json:"name"`
	Path         string            `json:"path"`
	Environment  map[string]string `json:"environment,omitempty"`
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
// Model, Effort and ContextTier are optional per-prompt overrides (empty = use
// the session's stored default).
type ContinueSessionData struct {
	SessionID       string   `json:"session_id"`
	Prompt          string   `json:"prompt"`
	Model           string   `json:"model,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	ContextTier     string   `json:"context_tier,omitempty"`
	Source          string   `json:"source,omitempty"`            // queue source marker (e.g. "callback") for queue_prompt
	SourceSessionID string   `json:"source_session_id,omitempty"` // James session that invoked this prompt through a gadget
	SourceName      string   `json:"source_name,omitempty"`       // resolved display name for SourceSessionID
	Attachments     []string `json:"attachments,omitempty"`       // absolute paths of saved attachments
}

// UpdateSessionData is the data payload for update_session.
// Only non-nil pointer fields are updated.
type UpdateSessionData struct {
	SessionID      string             `json:"session_id"`
	Name           *string            `json:"name,omitempty"`
	SystemPrompt   *string            `json:"system_prompt,omitempty"`
	Model          *string            `json:"model,omitempty"`
	Effort         *string            `json:"effort,omitempty"`
	ContextTier    *string            `json:"context_tier,omitempty"`
	Yolo           *bool              `json:"yolo,omitempty"`
	Path           *string            `json:"path,omitempty"`
	CompactionMode *string            `json:"compaction_mode,omitempty"`
	Environment    *map[string]string `json:"environment,omitempty"`
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

// GetLogsData requests the newest daemon log lines. Lines defaults to 100.
type GetLogsData struct {
	Lines int `json:"lines,omitempty"`
}

// GetLogsResponse contains the requested tail of the daemon log.
// Truncated is true when the final log lines exceeded the response byte limit.
type GetLogsResponse struct {
	Content   string `json:"content"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
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
	ContextTier  string `json:"context_tier,omitempty"`
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
	ContextTokens int               `json:"context_tokens,omitempty"`
	ContextWindow int               `json:"context_window,omitempty"`
	OpenCodeCost  float64           `json:"opencode_cost,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
}

// SessionConversation is returned by get_session_conversation.
type SessionConversation struct {
	SessionID    string             `json:"session_id"`
	Conversation []ConversationTurn `json:"conversation"`
	Total        int                `json:"total"` // total number of turns in the session
}

// ConversationTurn represents a single prompt/response pair.
type ConversationTurn struct {
	Role            string `json:"role"` // "user" or "assistant"
	Content         string `json:"content"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	SourceName      string `json:"source_name,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
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
// standalone summary. By default the session's own configured agent generates
// the summary, but the optional override fields let a caller (e.g. copy-session
// duplicating to a different agent) run the one-shot with the target agent
// instead — useful when the source agent is unavailable. All override fields
// are optional; empty/nil means "use the source session's value".
type SummarizeSessionData struct {
	SessionID   string `json:"session_id"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	ContextTier string `json:"context_tier,omitempty"`
	// Yolo is a pointer so an absent field inherits the source's yolo setting,
	// while an explicit true/false overrides it.
	Yolo *bool `json:"yolo,omitempty"`
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

// CreateDirectoryData is the data payload for create_directory. Path is the
// parent directory; Name is the new folder to create inside it. If Name is
// empty, Path itself is created.
type CreateDirectoryData struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// CreateDirectoryResponse is returned by create_directory with the resolved
// absolute path of the created directory.
type CreateDirectoryResponse struct {
	Path string `json:"path"`
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

// SaveAttachmentData is the data payload for save_attachment. Content is the
// base64-encoded file bytes; the moneypenny writes it into the session's
// attachments directory and returns the resolved absolute path.
type SaveAttachmentData struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Content   string `json:"content"` // base64-encoded file content
}

// SaveAttachmentResponse is returned by save_attachment.
type SaveAttachmentResponse struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ScheduleData is the data payload for schedule.
type ScheduleData struct {
	SessionID   string `json:"session_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"` // RFC3339 UTC
	CronExpr    string `json:"cron_expr,omitempty"`
	// ReplyChannelID routes the scheduled prompt's output to a channel
	// (channels.id). 0 = no channel routing.
	ReplyChannelID int64 `json:"reply_channel_id,omitempty"`
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
	ID             int64  `json:"id"`
	SessionID      string `json:"session_id"`
	Prompt         string `json:"prompt"`
	ScheduledAt    string `json:"scheduled_at"`
	Status         string `json:"status"`
	CronExpr       string `json:"cron_expr,omitempty"`
	ReplyChannelID int64  `json:"reply_channel_id,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// ListSchedulesResponse is returned by list_schedules.
type ListSchedulesResponse struct {
	Schedules []ScheduleInfo `json:"schedules"`
}

// CancelScheduleData is the data payload for cancel_schedule.
type CancelScheduleData struct {
	ScheduleID int64 `json:"schedule_id"`
}

// UpdateScheduleData is the data payload for update_schedule. It edits an
// existing pending schedule in place (preserving its ID).
type UpdateScheduleData struct {
	ScheduleID  int64  `json:"schedule_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"` // RFC3339 UTC
	CronExpr    string `json:"cron_expr,omitempty"`
	// ReplyChannelID routes the scheduled prompt's output to a channel
	// (channels.id). 0 = no channel routing.
	ReplyChannelID int64 `json:"reply_channel_id,omitempty"`
}

// UpdateScheduleResponse is returned by update_schedule on success.
type UpdateScheduleResponse struct {
	ScheduleID  int64  `json:"schedule_id"`
	SessionID   string `json:"session_id"`
	ScheduledAt string `json:"scheduled_at"`
}

// GitCommitData is the data payload for git_commit.
type GitCommitData struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Amend     bool   `json:"amend,omitempty"`
	NoEdit    bool   `json:"no_edit,omitempty"`
	// Files, when non-empty, restricts the commit to the given pathspecs
	// (only these are staged and committed) instead of all changes.
	Files []string `json:"files,omitempty"`
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

// --- Channels: bind a session to external communication (Teams, ...) ---

// ChannelProviderInfo describes an available channel provider and the target
// selection methods it supports.
type ChannelProviderInfo struct {
	Name      string `json:"name"`
	Search    bool   `json:"search"`     // supports search_channel_targets
	ProvideID bool   `json:"provide_id"` // supports directly-provided target ids
}

// ListChannelProvidersResponse is returned by list_channel_providers.
type ListChannelProvidersResponse struct {
	Providers []ChannelProviderInfo `json:"providers"`
}

// SearchChannelTargetsData is the payload for search_channel_targets.
type SearchChannelTargetsData struct {
	Provider string `json:"provider"`
	Query    string `json:"query"`
}

// ChannelTargetInfo is a candidate target within a provider (a Teams chat, ...).
type ChannelTargetInfo struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// SearchChannelTargetsResponse is returned by search_channel_targets.
type SearchChannelTargetsResponse struct {
	Targets []ChannelTargetInfo `json:"targets"`
}

// CreateChannelData is the payload for create_channel. When TargetLabel is
// empty and the provider supports id resolution, moneypenny resolves a label.
type CreateChannelData struct {
	SessionID   string `json:"session_id"`
	Provider    string `json:"provider"`
	TargetID    string `json:"target_id"`
	TargetLabel string `json:"target_label,omitempty"`
	Mention     string `json:"mention,omitempty"`
	AllowAnyone bool   `json:"allow_anyone,omitempty"`
}

// ChannelInfo represents a channel binding in responses.
type ChannelInfo struct {
	ID           int64  `json:"id"`
	SessionID    string `json:"session_id"`
	Provider     string `json:"provider"`
	TargetID     string `json:"target_id"`
	TargetLabel  string `json:"target_label"`
	Enabled      bool   `json:"enabled"`
	Mention      string `json:"mention"`
	AllowAnyone  bool   `json:"allow_anyone"`
	LastError    string `json:"last_error,omitempty"`
	LastActivity string `json:"last_activity,omitempty"` // RFC3339 UTC, empty if none
	CreatedAt    string `json:"created_at"`
}

// CreateChannelResponse is returned by create_channel.
type CreateChannelResponse struct {
	Channel ChannelInfo `json:"channel"`
}

// ListChannelsData is the payload for list_channels (empty session = all).
type ListChannelsData struct {
	SessionID string `json:"session_id,omitempty"`
}

// ListChannelsResponse is returned by list_channels.
type ListChannelsResponse struct {
	Channels []ChannelInfo `json:"channels"`
}

// ChannelIDData is a payload carrying just a channel id.
type ChannelIDData struct {
	ChannelID int64 `json:"channel_id"`
}

// SetChannelEnabledData is the payload for set_channel_enabled.
type SetChannelEnabledData struct {
	ChannelID int64 `json:"channel_id"`
	Enabled   bool  `json:"enabled"`
}

// UpdateChannelData is the payload for update_channel. Fields are pointers so a
// nil value leaves that attribute unchanged (partial update).
type UpdateChannelData struct {
	ChannelID   int64   `json:"channel_id"`
	Mention     *string `json:"mention,omitempty"`
	AllowAnyone *bool   `json:"allow_anyone,omitempty"`
}
