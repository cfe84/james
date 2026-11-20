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
	"time"

	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

func (h *Handler) publishArtifact(ctx context.Context, sessionID string, raw json.RawMessage) (any, error) {
	var request artifact.PublishRequest
	if err := decodeGadget(raw, &request); err != nil {
		return nil, err
	}
	if request.Project {
		return h.routeGadget(ctx, sessionID, "artifacts.publish", raw)
	}
	return h.snapshotArtifact(ctx, sessionID, "", request.Path, request.Title, nil)
}

func (h *Handler) updateArtifact(ctx context.Context, sessionID string, raw json.RawMessage) (any, error) {
	var request artifact.UpdateRequest
	if err := decodeGadget(raw, &request); err != nil {
		return nil, err
	}
	if !sessionIDPattern.MatchString(request.ID) {
		return nil, &gadgetError{"invalid_request", "invalid artifact ID"}
	}
	if request.Project {
		return h.routeGadget(ctx, sessionID, "artifacts.update", raw)
	}
	return h.snapshotArtifact(ctx, sessionID, request.ID, request.Path, "", nil)
}

func (h *Handler) deleteArtifactGadget(ctx context.Context, sessionID string, raw json.RawMessage) (any, error) {
	var request artifact.DeleteRequest
	if err := decodeGadget(raw, &request); err != nil {
		return nil, err
	}
	if !sessionIDPattern.MatchString(request.ID) {
		return nil, &gadgetError{"invalid_request", "invalid artifact ID"}
	}
	if request.Project {
		return h.routeGadget(ctx, sessionID, "artifacts.delete", raw)
	}
	return h.removeArtifact(artifact.Ref{SessionID: sessionID, ID: request.ID}, false, true)
}

func (h *Handler) stageArtifact(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var request artifact.StageRequest
	if err := decodeGadget(cmd.Data, &request); err != nil ||
		(request.Name != "" && sanitizeAttachmentName(request.Name) != request.Name) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid staged artifact request")
	}
	meta, err := h.snapshotArtifact(ctx, request.SessionID, "", request.Path, request.Title, &request)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, meta)
}

type storedArtifact struct {
	artifact.Metadata
	Revision string `json:"revision,omitempty"`
}

func (a storedArtifact) contentName() string {
	if a.Revision == "" {
		return "content"
	}
	return "content-" + a.Revision
}

func loadStoredArtifact(dir string) (storedArtifact, error) {
	var meta storedArtifact
	f, err := os.Open(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return meta, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return meta, err
	}
	if len(raw) > 4096 {
		return meta, fmt.Errorf("artifact metadata exceeds size limit")
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return meta, err
	}
	if meta.ID != filepath.Base(dir) || meta.Size < 0 || meta.Size > artifact.MaxFileSize ||
		(meta.Revision != "" && !sessionIDPattern.MatchString(meta.Revision)) {
		return meta, fmt.Errorf("invalid artifact metadata")
	}
	return meta, nil
}

func (h *Handler) snapshotArtifact(ctx context.Context, sessionID, id, source, title string, stage *artifact.StageRequest) (any, error) {
	if strings.TrimSpace(source) == "" || len(title) > 240 {
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
	root = filepath.Join(root, artifactDir(stage != nil))
	if stage != nil {
		// At most one temporary snapshot per session; recover an abandoned transfer.
		if err := os.RemoveAll(root); err != nil {
			return nil, fmt.Errorf("cleaning abandoned artifact transfer: %w", err)
		}
	}
	var previous storedArtifact
	if id != "" {
		dir := filepath.Join(root, id)
		previous, err = loadStoredArtifact(dir)
		if err != nil {
			return nil, fmt.Errorf("looking up artifact in this session: %w", err)
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			name := file.Name()
			if name != previous.contentName() && (name == "content" || strings.HasPrefix(name, "content-")) {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					return nil, fmt.Errorf("cleaning superseded artifact content: %w", err)
				}
			}
		}
	}
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
		if entry.Name() == id {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		saved, err := loadStoredArtifact(dir)
		if err != nil {
			return nil, fmt.Errorf("checking artifact capacity: %w", err)
		}
		stat, err := os.Stat(filepath.Join(dir, saved.contentName()))
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
	if stage != nil && stage.Name != "" {
		name = stage.Name
	}
	meta := storedArtifact{Metadata: artifact.Metadata{ID: newAgentSessionID(), Name: name, Title: title}}
	if id != "" {
		meta = previous
		meta.Revision = newAgentSessionID()
	}
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
	target := filepath.Join(dir, meta.contentName())
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
	// Older text artifacts had no preview format; their preserved filename determines it.
	addedTextPreview := previous.View == "" && (meta.View == "markdown" || meta.View == "text")
	if id != "" && meta.View != previous.View && !addedTextPreview {
		return nil, fmt.Errorf("replacement must keep the original artifact's preview format")
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), encoded, 0600); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	destination := filepath.Join(root, meta.ID)
	if id != "" {
		replacement := filepath.Join(destination, meta.contentName())
		if err := os.Rename(target, replacement); err != nil {
			return nil, err
		}
		// Metadata is the commit point: readers see either complete snapshot.
		if err := os.Rename(filepath.Join(dir, "metadata.json"), filepath.Join(destination, "metadata.json")); err != nil {
			h.removeArtifactFile(sessionID, replacement)
			return nil, err
		}
		h.removeArtifactFile(sessionID, filepath.Join(destination, previous.contentName()))
		return meta.Metadata, nil
	}
	if err := os.Rename(dir, destination); err != nil {
		return nil, err
	}
	dir = destination
	if stage == nil {
		if err := h.store.AddConversationTurn(sessionID, "artifact", string(encoded)); err != nil {
			return nil, err
		}
	}
	committed = true
	return meta.Metadata, nil
}

func artifactDir(staged bool) string {
	if staged {
		return "artifact-stage"
	}
	return "artifacts"
}

func (h *Handler) removeArtifactFile(sessionID, filename string) {
	if err := os.Remove(filename); err != nil {
		h.errorLog("failed to remove superseded artifact content for session %s: %v", sessionID, err)
	}
}

func (h *Handler) listArtifacts(cmd *envelope.Command) *envelope.Response {
	var request envelope.SessionIDData
	if err := decodeGadget(cmd.Data, &request); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	dir, err := h.sessionDirPath(request.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	h.artifactsMu.Lock()
	defer h.artifactsMu.Unlock()
	fail := func(err error) *envelope.Response {
		h.errorLog("listing artifacts for session %s: %v", request.SessionID, err)
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "could not list session artifacts")
	}
	session, err := h.store.GetSession(request.SessionID)
	if err != nil {
		return fail(err)
	}
	if session == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, "artifact session not found")
	}
	root := filepath.Join(dir, "artifacts")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	items := []artifact.Metadata{}
	for _, entry := range entries {
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) {
			continue
		}
		meta, err := loadStoredArtifact(filepath.Join(root, entry.Name()))
		if err != nil {
			return fail(err)
		}
		items = append(items, meta.Metadata)
	}
	if len(items) > 0 {
		times, err := h.store.ArtifactSentTimes(request.SessionID)
		if err != nil {
			return fail(err)
		}
		for i := range items {
			if sent := times[items[i].ID]; !sent.IsZero() {
				items[i].SentAt = sent.UTC().Format(time.RFC3339)
			}
		}
	}
	return envelope.SuccessResponse(cmd.RequestID, items)
}

func (h *Handler) deleteArtifact(cmd *envelope.Command) *envelope.Response {
	var request artifact.Ref
	if err := decodeGadget(cmd.Data, &request); err != nil || !sessionIDPattern.MatchString(request.ID) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid artifact delete request")
	}
	result, err := h.removeArtifact(request, cmd.Method == "delete_staged_artifact", false)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, result)
}

func (h *Handler) removeArtifact(request artifact.Ref, staged, requireOwned bool) (artifact.Ref, error) {
	dir, err := h.sessionDirPath(request.SessionID)
	if err != nil {
		return request, err
	}
	h.artifactsMu.Lock()
	defer h.artifactsMu.Unlock()
	fail := func(err error) (artifact.Ref, error) {
		h.errorLog("deleting artifact for session %s: %v", request.SessionID, err)
		return request, fmt.Errorf("could not delete session artifact")
	}
	session, err := h.store.GetSession(request.SessionID)
	if err != nil {
		return fail(err)
	}
	if session == nil {
		return request, fmt.Errorf("artifact session not found")
	}
	target := filepath.Join(dir, artifactDir(staged), request.ID)
	if requireOwned {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			// A previous delete may have removed the bytes but failed to remove the card.
			times, err := h.store.ArtifactSentTimes(request.SessionID)
			if err != nil {
				return fail(err)
			}
			if _, published := times[request.ID]; !published {
				return request, &gadgetError{"not_found", "artifact not found in your session"}
			}
		} else if err != nil {
			return fail(err)
		}
	}
	if err := os.RemoveAll(target); err != nil {
		return fail(err)
	}
	if !staged {
		if err := h.store.DeleteArtifactTurn(request.SessionID, request.ID); err != nil {
			return fail(err)
		}
	}
	return request, nil
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
	staged := cmd.Method == "read_staged_artifact"
	root = filepath.Join(root, artifactDir(staged), request.ID)
	fail := func(err error) *envelope.Response {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("reading artifact: %v", err))
	}
	meta, err := loadStoredArtifact(root)
	if err != nil {
		return fail(err)
	}
	if request.Offset > meta.Size {
		return fail(fmt.Errorf("invalid artifact offset"))
	}
	if request.MetadataOnly {
		return envelope.SuccessResponse(cmd.RequestID, artifact.Chunk{Artifact: meta.Metadata})
	}
	in, err := os.Open(filepath.Join(root, meta.contentName()))
	if err != nil {
		return fail(err)
	}
	defer in.Close()
	if _, err := in.Seek(request.Offset, io.SeekStart); err != nil {
		return fail(err)
	}
	chunkSize := int64(artifact.ChunkSize)
	if staged {
		chunkSize = artifact.StageChunkSize
	}
	content, err := io.ReadAll(io.LimitReader(in, min(chunkSize, meta.Size-request.Offset)))
	if err != nil {
		return fail(err)
	}
	return envelope.SuccessResponse(cmd.RequestID, artifact.Chunk{Artifact: meta.Metadata, Content: content})
}
