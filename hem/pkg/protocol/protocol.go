package protocol

import "encoding/json"

// Request is sent from the CLI client to the server over the Unix socket.
type Request struct {
	Verb      string   `json:"verb"`
	Noun      string   `json:"noun"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id,omitempty"`
}

// Response is sent from the server back to the CLI client.
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
