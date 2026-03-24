package envelope

import (
	"encoding/json"
	"fmt"
)

// Message types
const (
	TypeCommand      = "request"
	TypeResponse     = "response"
	TypeNotification = "notification"
)

// Status values
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Error codes
const (
	ErrSessionNotFound      = "SESSION_NOT_FOUND"
	ErrSessionAlreadyExists = "SESSION_ALREADY_EXISTS"
	ErrSessionNotIdle       = "SESSION_NOT_IDLE"
	ErrSessionNotWorking    = "SESSION_NOT_WORKING"
	ErrAgentNotFound        = "AGENT_NOT_FOUND"
	ErrInvalidPath          = "INVALID_PATH"
	ErrAgentError           = "AGENT_ERROR"
	ErrInvalidRequest       = "INVALID_REQUEST"
	ErrInternalError        = "INTERNAL_ERROR"
)

// Command represents an incoming command envelope.
type Command struct {
	Type      string          `json:"type"`
	Method    string          `json:"method"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

// Response represents an outgoing response envelope.
type Response struct {
	Type      string      `json:"type"`
	Status    string      `json:"status"`
	RequestID string      `json:"request_id"`
	ErrorCode string      `json:"error_code,omitempty"`
	Data      interface{} `json:"data"`
}

// Notification represents an asynchronous event notification sent to hem.
type Notification struct {
	Type      string      `json:"type"` // always "notification"
	Event     string      `json:"event"`
	SessionID string      `json:"session_id"`
	Data      interface{} `json:"data,omitempty"`
}

// SuccessResponse creates a success response for the given request.
func SuccessResponse(requestID string, data interface{}) *Response {
	return &Response{
		Type:      TypeResponse,
		Status:    StatusSuccess,
		RequestID: requestID,
		Data:      data,
	}
}

// ErrorResponse creates an error response for the given request.
func ErrorResponse(requestID string, errorCode string, message string) *Response {
	return &Response{
		Type:      TypeResponse,
		Status:    StatusError,
		RequestID: requestID,
		ErrorCode: errorCode,
		Data:      map[string]string{"message": message},
	}
}

// ParseCommand parses a JSON line into a Command. Returns an error if the JSON
// is invalid or if the type is not "command".
func ParseCommand(data []byte) (*Command, error) {
	var cmd Command
	if err := json.Unmarshal(data, &cmd); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if cmd.Type != TypeCommand {
		return nil, fmt.Errorf("expected type %q, got %q", TypeCommand, cmd.Type)
	}
	if cmd.Method == "" {
		return nil, fmt.Errorf("missing method field")
	}
	if cmd.RequestID == "" {
		return nil, fmt.Errorf("missing request_id field")
	}
	return &cmd, nil
}

// Marshal serializes a response to JSON bytes (with a trailing newline for line-based protocol).
func (r *Response) Marshal() ([]byte, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// NewNotification creates a notification for an asynchronous event.
func NewNotification(event, sessionID string, data interface{}) *Notification {
	return &Notification{
		Type:      TypeNotification,
		Event:     event,
		SessionID: sessionID,
		Data:      data,
	}
}

// Marshal serializes a notification to JSON bytes (with a trailing newline for line-based protocol).
func (n *Notification) Marshal() ([]byte, error) {
	b, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// NotificationWriter sends notifications to an output stream (stdout/MI6).
type NotificationWriter struct {
	writer interface{ Write([]byte) (int, error) }
}

// NewNotificationWriter creates a notification writer that writes to the given writer.
func NewNotificationWriter(w interface{ Write([]byte) (int, error) }) *NotificationWriter {
	return &NotificationWriter{writer: w}
}

// Send sends a notification to the output stream.
func (nw *NotificationWriter) Send(event, sessionID string, data interface{}) error {
	if nw == nil || nw.writer == nil {
		return nil // No-op if writer not set
	}
	notification := NewNotification(event, sessionID, data)
	b, err := notification.Marshal()
	if err != nil {
		return err
	}
	_, err = nw.writer.Write(b)
	return err
}
