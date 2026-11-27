package handler

import (
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
	"james/moneypenny/pkg/store"
)

const artifactCopyDir = "artifact-copy"

func (h *Handler) artifactCopyPath(sessionID string) (string, error) {
	if _, err := h.sessionDirPath(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(h.dataDir, artifactCopyDir, sessionID), nil
}

func (h *Handler) stageArtifactCopy(cmd *envelope.Command) *envelope.Response {
	fail := func(err error) *envelope.Response {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	var request artifact.CopyChunk
	if err := decodeGadget(cmd.Data, &request); err != nil {
		return fail(err)
	}
	meta := request.Artifact
	hash, err := hex.DecodeString(meta.SHA256)
	if !sessionIDPattern.MatchString(meta.ID) || meta.Name == "" || sanitizeAttachmentName(meta.Name) != meta.Name ||
		strings.ContainsAny(meta.Name, "\r\n") || len(meta.Title) > 240 || meta.ProjectID != "" ||
		err != nil || len(hash) != sha256.Size || meta.Size < 0 || meta.Size > artifact.MaxFileSize ||
		request.Offset < 0 || request.Offset > meta.Size || len(request.Content) > artifact.ChunkSize ||
		int64(len(request.Content)) > meta.Size-request.Offset || (len(request.Content) == 0 && meta.Size != 0) {
		return fail(fmt.Errorf("invalid artifact copy chunk"))
	}
	staging, err := h.artifactCopyPath(request.SessionID)
	if err != nil {
		return fail(err)
	}
	session, err := h.store.GetSession(request.SessionID)
	if err != nil {
		return fail(err)
	}
	if session != nil {
		return fail(fmt.Errorf("artifact copy requires a new session"))
	}
	if err := appendArtifactCopy(staging, request); err != nil {
		return fail(err)
	}
	return envelope.SuccessResponse(cmd.RequestID, nil)
}

func appendArtifactCopy(root string, request artifact.CopyChunk) error {
	meta := request.Artifact
	dir := filepath.Join(root, meta.ID)
	flags := os.O_WRONLY | os.O_APPEND
	if request.Offset == 0 {
		if err := reserveArtifactCopy(root); err != nil {
			return err
		}
		if err := os.MkdirAll(root, 0700); err != nil {
			return err
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		var total int64
		for _, entry := range entries {
			saved, err := loadStoredArtifact(filepath.Join(root, entry.Name()))
			if err != nil {
				return err
			}
			total += saved.Size
		}
		if len(entries) >= artifact.MaxFiles || total+meta.Size > artifact.MaxSessionSize {
			return fmt.Errorf("session artifact capacity exceeded")
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0600); err != nil {
			return err
		}
		flags |= os.O_CREATE | os.O_EXCL
	} else {
		saved, err := loadStoredArtifact(dir)
		if err != nil {
			return err
		}
		if saved.Metadata != meta {
			return fmt.Errorf("artifact metadata changed during copy")
		}
	}
	out, err := os.OpenFile(filepath.Join(dir, "content"), flags, 0600)
	if err != nil {
		return err
	}
	stat, err := out.Stat()
	if err == nil && stat.Size() != request.Offset {
		err = fmt.Errorf("unexpected artifact copy offset")
	}
	if err == nil {
		_, err = out.Write(request.Content)
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

func reserveArtifactCopy(root string) error {
	parent := filepath.Dir(root)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(root) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		// Hem bounds transfers to five minutes. Reclaim a slot abandoned by a lost Hem.
		if !entry.IsDir() || !sessionIDPattern.MatchString(entry.Name()) || time.Since(info.ModTime()) < 15*time.Minute {
			return fmt.Errorf("another artifact copy is staged on this Moneypenny")
		}
		if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) discardArtifactCopy(cmd *envelope.Command) *envelope.Response {
	var request envelope.SessionIDData
	if err := decodeGadget(cmd.Data, &request); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	staging, err := h.artifactCopyPath(request.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	if err := os.RemoveAll(staging); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, nil)
}

func (h *Handler) installArtifactCopies(sessionID string, ids []string) (string, []*store.ConversationTurn, error) {
	if len(ids) == 0 {
		return "", nil, nil
	}
	dir, err := h.sessionDirPath(sessionID)
	if err != nil {
		return "", nil, err
	}
	staging, err := h.artifactCopyPath(sessionID)
	if err != nil {
		return "", nil, err
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", nil, err
	}
	if len(ids) != len(entries) || len(ids) > artifact.MaxFiles {
		return "", nil, fmt.Errorf("incomplete artifact copy")
	}
	turns := make([]*store.ConversationTurn, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !sessionIDPattern.MatchString(id) || seen[id] {
			return "", nil, fmt.Errorf("invalid copied artifact ID")
		}
		seen[id] = true
		item := filepath.Join(staging, id)
		meta, err := loadStoredArtifact(item)
		if err != nil {
			return "", nil, err
		}
		file := filepath.Join(item, meta.contentName())
		if err := verifyArtifactCopy(file, meta.Metadata); err != nil {
			return "", nil, err
		}
		meta.View = artifact.Inspect(file, meta.Name)
		at := time.Now().UTC()
		if meta.SentAt != "" {
			at, err = time.Parse(time.RFC3339, meta.SentAt)
			if err != nil {
				return "", nil, err
			}
		}
		meta.SentAt = ""
		raw, err := json.Marshal(meta)
		if err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(filepath.Join(item, "metadata.json"), raw, 0600); err != nil {
			return "", nil, err
		}
		turns = append(turns, &store.ConversationTurn{Role: "artifact", Content: string(raw), CreatedAt: at})
	}
	destination := filepath.Join(dir, "artifacts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", nil, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", nil, fmt.Errorf("destination already contains artifacts")
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", nil, err
	}
	return destination, turns, nil
}

func verifyArtifactCopy(path string, meta artifact.Metadata) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(in, meta.Size+1))
	if err != nil {
		return err
	}
	if n != meta.Size || hex.EncodeToString(hash.Sum(nil)) != meta.SHA256 {
		return fmt.Errorf("copied artifact size or checksum mismatch")
	}
	return nil
}
