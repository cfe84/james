package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

// distillPrompt asks the agent to read the full transcript and fold all durable
// knowledge into hierarchical memory. Unlike compaction's distill prompt (which
// relies on the live agent's accumulated context), distillation runs over a
// fresh agent session and is therefore handed the transcript explicitly.
const distillPrompt = `[SYSTEM: MEMORY DISTILLATION]
Below is the full transcript of a conversation. Your job is to extract ALL durable, important information from it into your memory folder. Do the following, in order:

1. First inspect your EXISTING memory (read its README.md and browse the topic folders) to see what is already recorded and how it is organized.

2. Then go through the transcript and SAVE everything important into memory by creating/editing README.md files in topic folders: the original task, key decisions and their rationale, important context (file paths, names, conventions, learnings), the current state of the work, and any pending actions. Where the transcript adds to or changes something already in memory, UPDATE the existing README.md rather than duplicating it. Reorganize the folders if that makes it clearer. Keep high-level synthesis in parent folders' README.md and detail in child folders so nothing is lost.

3. When done, briefly report what you wrote or updated as your final message.

Transcript:
%s`

// cleanTranscript renders a conversation as a plain USER/ASSISTANT/SYSTEM
// transcript, skipping ephemeral thinking/agent_text/tool noise.
func cleanTranscript(turns []*store.ConversationTurn) string {
	var b strings.Builder
	for _, t := range turns {
		switch t.Role {
		case "user", "scheduled":
			b.WriteString("USER: ")
		case "assistant":
			b.WriteString("ASSISTANT: ")
		case "system":
			b.WriteString("SYSTEM: ")
		default:
			continue
		}
		b.WriteString(strings.TrimSpace(t.Content))
		b.WriteString("\n\n")
	}
	return b.String()
}

// distillSessionCmd is the dispatch for distill_session. It kicks off an
// asynchronous distillation: an agent run (same agent/model/effort as the
// session, but a throwaway underlying agent session so the live one is left
// untouched) that reads the full transcript and writes everything important
// into the session's hierarchical memory. Requires the session to be idle.
func (h *Handler) distillSessionCmd(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.DistillSessionData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id is required")
	}
	sess, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to get session: %v", err))
	}
	if sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}
	if sess.Status != store.StateIdle {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotIdle, fmt.Sprintf("session is not idle: %s", sess.Status))
	}

	if err := h.store.UpdateSessionStatus(data.SessionID, store.StateWorking); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventChatStatus, data.SessionID, map[string]string{
			"status": store.StateWorking,
			"reason": "distilling",
		})
	}

	go h.runDistillation(data.SessionID)

	return envelope.SuccessResponse(cmd.RequestID, envelope.DistillSessionResponse{SessionID: data.SessionID})
}

// runDistillation runs the distillation agent synchronously (called in its own
// goroutine by distillSessionCmd). It runs with NoPersistTurns so the agent's
// thinking/intermediate-text events are NOT written as conversation turns — the
// only durable side effect is whatever the agent writes to memory via its
// tools — so the live transcript is left clean. The caller must have set the
// session status to working; this resets it to idle when finished.
func (h *Handler) runDistillation(sessionID string) {
	defer func() {
		_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)
		if h.notifyWriter != nil {
			_ = h.notifyWriter.Send(envelope.EventSessionStateChanged, sessionID, map[string]string{
				"status": store.StateIdle,
				"reason": "distilled",
			})
			_ = h.notifyWriter.Send(envelope.EventChatStatus, sessionID, map[string]string{
				"status": store.StateIdle,
				"reason": "distilled",
			})
		}
	}()

	sess, err := h.store.GetSession(sessionID)
	if err != nil || sess == nil {
		h.vlog("distillation: cannot load session %s: %v", sessionID, err)
		return
	}
	h.ensureMemoryMigrated(sessionID)

	turns, err := h.store.GetConversation(sessionID)
	if err != nil {
		h.vlog("distillation: cannot load conversation for session %s: %v", sessionID, err)
		return
	}
	transcript := cleanTranscript(turns)
	if strings.TrimSpace(transcript) == "" {
		h.vlog("distillation: nothing to distill for session %s (empty transcript)", sessionID)
		return
	}

	// Run the agent with the session's system prompt plus the file-based memory
	// instructions and a body-less outline of the current memory tree, so it can
	// target/extend existing nodes. Use a throwaway underlying agent session id
	// (NOT persisted) so the live agent session is untouched.
	systemPrompt := sess.SystemPrompt
	memDir := h.memoryDir(sessionID)
	if !agent.MemoryEnabled(sess.Agent, sess.Yolo) {
		memDir = ""
	}
	if memDir != "" {
		systemPrompt += memorySystemPrompt(memDir)
	}

	params := agent.RunParams{
		SessionID:      sessionID,
		Agent:          sess.Agent,
		Prompt:         fmt.Sprintf(distillPrompt, transcript),
		SystemPrompt:   systemPrompt,
		Model:          sess.Model,
		Effort:         sess.Effort,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Resume:         false,
		AgentSessionID: newAgentSessionID(),
		SessionDir:     h.sessionDir(sessionID),
		MemoryDir:      memDir,
		NoPersistTurns: true,
	}

	if _, runErr := h.runner.Run(context.Background(), params); runErr != nil {
		h.vlog("distillation agent failed for session %s: %v", sessionID, runErr)
	} else {
		h.vlog("distillation completed for session %s", sessionID)
	}
}
