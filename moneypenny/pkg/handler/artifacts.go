package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

func (h *Handler) publishArtifact(ctx context.Context, sessionID string, raw json.RawMessage) (any, error) {
	if !json.Valid(raw) {
		return nil, &gadgetError{"invalid_request", "artifact publication requires one JSON object"}
	}
	var request struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	if err := decodeGadget(raw, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Path) == "" || len(request.Title) > 240 {
		return nil, fmt.Errorf("artifact path is required and title must not exceed 240 bytes")
	}
	h.artifactsMu.Lock()
	defer h.artifactsMu.Unlock()
	session, err := h.store.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session no longer exists")
	}
	source := request.Path
	if !filepath.IsAbs(source) {
		source = filepath.Join(session.Path, source)
	}
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("checking artifact source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > artifact.MaxFileSize {
		return nil, fmt.Errorf("artifact must be a regular file of at most %d bytes", artifact.MaxFileSize)
	}
	in, err := os.Open(source)
	if err != nil {
		return nil, fmt.Errorf("opening artifact: %w", err)
	}
	defer in.Close()
	info, err = in.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > artifact.MaxFileSize {
		return nil, fmt.Errorf("artifact must be a regular file of at most %d bytes", artifact.MaxFileSize)
	}
	root, err := h.sessionDirPath(sessionID)
	if err != nil {
		return nil, err
	}
	root = filepath.Join(root, "artifacts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var used int64
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pending-") {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				h.errorLog("failed to remove interrupted artifact for session %s: %v", sessionID, err)
				return nil, fmt.Errorf("cleaning interrupted publication: %w", err)
			}
			continue
		}
		stat, err := os.Stat(filepath.Join(root, entry.Name(), "content"))
		if err != nil {
			return nil, fmt.Errorf("checking artifact capacity: %w", err)
		}
		used += stat.Size()
		count++
	}
	if count >= artifact.MaxFiles || used+info.Size() > artifact.MaxSessionSize {
		return nil, fmt.Errorf("session artifact capacity exceeded")
	}
	name := sanitizeAttachmentName(info.Name())
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return nil, fmt.Errorf("invalid artifact filename")
	}
	meta := artifact.Metadata{ID: newAgentSessionID(), Name: name, Title: request.Title}
	if meta.Title == "" {
		meta.Title = meta.Name
	}
	dir, err := os.MkdirTemp(root, ".pending-")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			if err := os.RemoveAll(dir); err != nil {
				h.errorLog("failed to remove incomplete artifact for session %s: %v", sessionID, err)
			}
		}
	}()
	target := filepath.Join(dir, "content")
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	meta.Size, err = io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, artifact.MaxFileSize+1))
	closeErr := out.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if meta.Size > artifact.MaxFileSize || used+meta.Size > artifact.MaxSessionSize {
		return nil, fmt.Errorf("artifact capacity exceeded while copying")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	meta.SHA256 = hex.EncodeToString(hash.Sum(nil))
	meta.View = artifact.Inspect(target, meta.Name)
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), encoded, 0600); err != nil {
		return nil, err
	}
	destination := filepath.Join(root, meta.ID)
	if err := os.Rename(dir, destination); err != nil {
		return nil, err
	}
	dir = destination
	if err := h.store.AddConversationTurn(sessionID, "artifact", string(encoded)); err != nil {
		return nil, err
	}
	committed = true
	return meta, nil
}

func (h *Handler) readArtifact(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var request artifact.ReadRequest
	if err := json.Unmarshal(cmd.Data, &request); err != nil || !sessionIDPattern.MatchString(request.ID) || request.Offset < 0 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid artifact read request")
	}
	h.artifactsMu.Lock()
	defer h.artifactsMu.Unlock()
	session, err := h.store.GetSession(request.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("looking up artifact session: %v", err))
	}
	if session == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, "artifact session not found")
	}
	root, err := h.sessionDirPath(request.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	root = filepath.Join(root, "artifacts", request.ID)
	fail := func(err error) *envelope.Response {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("reading artifact: %v", err))
	}
	metadataFile, err := os.Open(filepath.Join(root, "metadata.json"))
	if err != nil {
		return fail(err)
	}
	defer metadataFile.Close()
	raw, err := io.ReadAll(io.LimitReader(metadataFile, 4097))
	if err != nil {
		return fail(err)
	}
	if len(raw) > 4096 {
		return fail(fmt.Errorf("artifact metadata exceeds size limit"))
	}
	var meta artifact.Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return fail(err)
	}
	if meta.ID != request.ID || meta.Size < 0 || meta.Size > artifact.MaxFileSize || request.Offset > meta.Size {
		return fail(fmt.Errorf("invalid artifact metadata or offset"))
	}
	if request.MetadataOnly {
		return envelope.SuccessResponse(cmd.RequestID, artifact.Chunk{Artifact: meta})
	}
	in, err := os.Open(filepath.Join(root, "content"))
	if err != nil {
		return fail(err)
	}
	defer in.Close()
	if _, err := in.Seek(request.Offset, io.SeekStart); err != nil {
		return fail(err)
	}
	content, err := io.ReadAll(io.LimitReader(in, min(artifact.ChunkSize, meta.Size-request.Offset)))
	if err != nil {
		return fail(err)
	}
	return envelope.SuccessResponse(cmd.RequestID, artifact.Chunk{Artifact: meta, Content: content})
}
