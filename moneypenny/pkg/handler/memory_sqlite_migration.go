package handler

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"james/moneypenny/pkg/memory"
)

// MigrateMemoryToSQLite migrates every registered session, including inactive
// sessions, before the daemon accepts requests. Other sessions are still tried
// when one fails. The caller must surface errors and retry failed migrations
// rather than treating their memory as empty.
func (h *Handler) MigrateMemoryToSQLite() error {
	sessions, err := h.store.ListSessions()
	if err != nil {
		return fmt.Errorf("list sessions for memory migration: %w", err)
	}
	var failures []error
	for _, session := range sessions {
		if err := h.MigrateSessionMemoryToSQLite(session.SessionID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// MigrateSessionMemoryToSQLite is an idempotent retry hook. Unlike previous
// migrations, it never modifies the main database or writes README files.
func (h *Handler) MigrateSessionMemoryToSQLite(sessionID string) error {
	dir, err := h.sessionDirPath(sessionID)
	if err != nil {
		return fmt.Errorf("memory migration session %q: %w", sessionID, err)
	}
	legacy, err := h.store.ListMemoryNodes(sessionID)
	if err != nil {
		return fmt.Errorf("read legacy memory session %q (safe to retry): %w", sessionID, err)
	}
	nodes := make([]*memory.Node, 0, len(legacy))
	for _, node := range legacy {
		nodes = append(nodes, &memory.Node{
			Path: node.Path, Title: node.Title, Description: node.Description, Body: node.Body,
		})
	}
	if len(nodes) == 0 {
		blob, err := h.store.GetMemory(sessionID)
		if err != nil {
			return fmt.Errorf("read legacy memory blob session %q (safe to retry): %w", sessionID, err)
		}
		if strings.TrimSpace(blob) != "" {
			nodes = append(nodes, &memory.Node{
				Path: "notes", Title: "Imported notes",
				Description: "Legacy memory imported from the previous flat note", Body: blob,
			})
		}
	}
	return memory.Migrate(filepath.Join(dir, "memory"), nodes)
}
