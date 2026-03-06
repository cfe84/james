package envelope

// CreateSessionData is the data payload for create_session.
type CreateSessionData struct {
	Agent        string `json:"agent"`
	SystemPrompt string `json:"system_prompt"`
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
	Yolo         bool   `json:"yolo"`
	Path         string `json:"path"`
	LastAccessed string `json:"last_accessed,omitempty"`
}

// SessionConversation is returned by get_session_conversation.
type SessionConversation struct {
	SessionID    string             `json:"session_id"`
	Conversation []ConversationTurn `json:"conversation"`
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
