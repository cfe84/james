package handler

import (
	"encoding/json"
	"os"
	"path/filepath"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/memory"
)

func (h *Handler) exportSessionMemory(cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil || !sessionIDPattern.MatchString(data.SessionID) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id must be a canonical UUID")
	}
	source, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	if source == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, "source session not found")
	}
	dir, err := h.sessionDirPath(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	snapshot, err := memory.ExportSnapshot(filepath.Join(dir, "memory"))
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, snapshot)
}

func reserveMemoryImport(root string) (*os.File, error) {
	path := memory.DatabasePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}
