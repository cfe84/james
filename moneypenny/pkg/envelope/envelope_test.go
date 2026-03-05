package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCommand_Valid(t *testing.T) {
	input := `{"type":"request","method":"create_session","request_id":"req-1","data":{"agent":"claude-code"}}`
	cmd, err := ParseCommand([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Type != TypeCommand {
		t.Errorf("expected type %q, got %q", TypeCommand, cmd.Type)
	}
	if cmd.Method != "create_session" {
		t.Errorf("expected method %q, got %q", "create_session", cmd.Method)
	}
	if cmd.RequestID != "req-1" {
		t.Errorf("expected request_id %q, got %q", "req-1", cmd.RequestID)
	}
	if cmd.Data == nil {
		t.Fatal("expected data to be non-nil")
	}
	// Verify data can be unmarshalled
	var data CreateSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if data.Agent != "claude-code" {
		t.Errorf("expected agent %q, got %q", "claude-code", data.Agent)
	}
}

func TestParseCommand_RejectsNonCommandType(t *testing.T) {
	input := `{"type":"response","method":"create_session","request_id":"req-1","data":{}}`
	_, err := ParseCommand([]byte(input))
	if err == nil {
		t.Fatal("expected error for non-command type")
	}
	if !strings.Contains(err.Error(), "expected type") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCommand_RejectsMissingMethod(t *testing.T) {
	input := `{"type":"request","request_id":"req-1","data":{}}`
	_, err := ParseCommand([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing method")
	}
	if !strings.Contains(err.Error(), "missing method") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCommand_RejectsMissingRequestID(t *testing.T) {
	input := `{"type":"request","method":"create_session","data":{}}`
	_, err := ParseCommand([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing request_id")
	}
	if !strings.Contains(err.Error(), "missing request_id") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCommand_RejectsInvalidJSON(t *testing.T) {
	_, err := ParseCommand([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSuccessResponse(t *testing.T) {
	resp := SuccessResponse("req-42", map[string]string{"session_id": "s-1"})
	if resp.Type != TypeResponse {
		t.Errorf("expected type %q, got %q", TypeResponse, resp.Type)
	}
	if resp.Status != StatusSuccess {
		t.Errorf("expected status %q, got %q", StatusSuccess, resp.Status)
	}
	if resp.RequestID != "req-42" {
		t.Errorf("expected request_id %q, got %q", "req-42", resp.RequestID)
	}
	if resp.ErrorCode != "" {
		t.Errorf("expected empty error_code, got %q", resp.ErrorCode)
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	// Verify error_code is omitted
	if strings.Contains(string(b), "error_code") {
		t.Error("success response should not contain error_code")
	}
	// Verify data is present
	if !strings.Contains(string(b), `"session_id":"s-1"`) {
		t.Errorf("expected data to contain session_id, got %s", string(b))
	}
}

func TestErrorResponse(t *testing.T) {
	resp := ErrorResponse("req-99", ErrSessionNotFound, "session not found")
	if resp.Type != TypeResponse {
		t.Errorf("expected type %q, got %q", TypeResponse, resp.Type)
	}
	if resp.Status != StatusError {
		t.Errorf("expected status %q, got %q", StatusError, resp.Status)
	}
	if resp.RequestID != "req-99" {
		t.Errorf("expected request_id %q, got %q", "req-99", resp.RequestID)
	}
	if resp.ErrorCode != ErrSessionNotFound {
		t.Errorf("expected error_code %q, got %q", ErrSessionNotFound, resp.ErrorCode)
	}

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if !strings.Contains(string(b), `"error_code":"SESSION_NOT_FOUND"`) {
		t.Errorf("expected error_code in JSON, got %s", string(b))
	}
	if !strings.Contains(string(b), `"message":"session not found"`) {
		t.Errorf("expected message in data, got %s", string(b))
	}
}

func TestResponseMarshal(t *testing.T) {
	resp := SuccessResponse("req-1", map[string]string{"ok": "true"})
	b, err := resp.Marshal()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must end with newline
	if b[len(b)-1] != '\n' {
		t.Error("expected trailing newline")
	}
	// Must be valid JSON (without the trailing newline)
	var check map[string]interface{}
	if err := json.Unmarshal(b[:len(b)-1], &check); err != nil {
		t.Fatalf("marshal output is not valid JSON: %v", err)
	}
	if check["type"] != TypeResponse {
		t.Errorf("expected type %q, got %v", TypeResponse, check["type"])
	}
	if check["status"] != StatusSuccess {
		t.Errorf("expected status %q, got %v", StatusSuccess, check["status"])
	}
}
