package protocol

import "encoding/json"

// Request is sent from the CLI client to the hem server over the Unix socket.
// This protocol is for CLI/TUI ↔ hem-server communication.
//
// Note: Similar Command/Response types exist in transport and envelope packages:
// - hem/pkg/protocol (this): CLI/TUI ↔ hem-server (Unix socket, verb+noun based)
// - hem/pkg/transport: hem-server ↔ moneypenny (FIFO/MI6, method based)
// - moneypenny/pkg/envelope: moneypenny internal protocol
//
// These are intentionally separate to maintain layer isolation and avoid cross-module dependencies.
type Request struct {
	Verb      string   `json:"verb"`
	Noun      string   `json:"noun"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id,omitempty"`
}

// Response is sent from the hem server back to the CLI client.
// Higher-level than transport.Response, with structured data for CLI formatting.
type Response struct {
	Status    string          `json:"status"`               // "ok" or "error"
	Message   string          `json:"message,omitempty"`    // error message (when status == "error")
	Data      json.RawMessage `json:"data,omitempty"`       // structured result data
	RequestID string          `json:"request_id,omitempty"` // echoed from request
}

const (
	StatusOK    = "ok"
	StatusError = "error"
)

// OKResponse creates a success response with structured data.
func OKResponse(data interface{}) *Response {
	b, _ := json.Marshal(data)
	return &Response{Status: StatusOK, Data: b}
}

// ErrResponse creates an error response.
func ErrResponse(msg string) *Response {
	return &Response{Status: StatusError, Message: msg}
}
