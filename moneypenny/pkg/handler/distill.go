package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

// distillPrompt asks the agent to read the full transcript and fold all durable
// knowledge into hierarchical memory. Unlike compaction's distill prompt (which
// relies on the live agent's accumulated context), distillation runs over a
// fresh agent session and is therefore handed the transcript explicitly.
const distillPrompt = `[SYSTEM: MEMORY DISTILLATION]
Below is the stored conversation transcript. Extract its durable knowledge and relevant working state according to the system memory contract: the original task, key decisions and rationale, important context (file paths, names, conventions, learnings), current state, and pending actions.

When done, briefly report what you wrote or updated as your final message.

Transcript:
%s`

// maxDistillationChunkBytes is the cap for the complete formatted prompt,
// including distillPrompt's wrapper, not just the transcript payload.
const maxDistillationChunkBytes = 64 * 1024

func distillableTurn(t *store.ConversationTurn) bool {
	switch t.Role {
	case "user", "assistant", "scheduled", "callback":
		return true
	default:
		return false
	}
}

func distillationPromptOverhead() int {
	return len(fmt.Sprintf(distillPrompt, ""))
}

func distillationSnapshotMaxID(turns []*store.ConversationTurn) int64 {
	var maxID int64
	for _, turn := range turns {
		if distillableTurn(turn) && turn.ID > maxID {
			maxID = turn.ID
		}
	}
	return maxID
}

func turnsThroughSnapshot(turns []*store.ConversationTurn, maxID int64) []*store.ConversationTurn {
	filtered := make([]*store.ConversationTurn, 0, len(turns))
	for _, turn := range turns {
		if turn.ID <= maxID {
			filtered = append(filtered, turn)
		}
	}
	return filtered
}

// distillationChunks treats maxPromptBytes as the cap for the complete
// formatted prompt, including the distillPrompt wrapper.
func distillationChunks(turns []*store.ConversationTurn, maxPromptBytes int) []string {
	if maxPromptBytes <= 0 {
		return nil
	}
	payloadBudget := maxPromptBytes - distillationPromptOverhead()
	if payloadBudget <= 0 {
		return nil
	}
	return distillationPayloadChunks(turns, payloadBudget)
}

func distillationPayloadChunks(turns []*store.ConversationTurn, payloadBudget int) []string {
	if payloadBudget <= 0 {
		return nil
	}
	var chunks []string
	var current strings.Builder
	for _, turn := range turns {
		if !distillableTurn(turn) {
			continue
		}
		line := cleanTranscript([]*store.ConversationTurn{turn})
		if line == "" {
			continue
		}
		if len(line) > payloadBudget {
			// Split only the content of an oversized turn at UTF-8 rune
			// boundaries. Labels make continuation chunks unambiguous.
			label := strings.TrimSpace(strings.SplitN(line, ":", 2)[0]) + ": "
			body := strings.TrimSpace(strings.TrimPrefix(line, label))
			prefix := label + "[continued] "
			suffix := "\n\n"
			if len(prefix)+len(suffix) < payloadBudget {
				for _, part := range splitUTF8WithinByteBudget(body, payloadBudget-len(prefix)-len(suffix)) {
					if current.Len() > 0 {
						chunks = append(chunks, current.String())
						current.Reset()
					}
					chunks = append(chunks, prefix+part+suffix)
				}
			} else {
				for _, part := range splitUTF8WithinByteBudget(line, payloadBudget) {
					if current.Len() > 0 {
						chunks = append(chunks, current.String())
						current.Reset()
					}
					chunks = append(chunks, part)
				}
			}
			continue
		}
		if current.Len() > 0 && current.Len()+len(line) > payloadBudget {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}
	return chunks
}

func splitUTF8WithinByteBudget(value string, budget int) []string {
	if budget <= 0 {
		return nil
	}
	var chunks []string
	for len(value) > 0 {
		n := 0
		for n < len(value) {
			_, size := utf8.DecodeRuneInString(value[n:])
			if n+size > budget {
				break
			}
			n += size
		}
		if n == 0 {
			break
		}
		chunks = append(chunks, value[:n])
		value = value[n:]
	}
	return chunks
}

// cleanTranscript renders a conversation as a plain USER/ASSISTANT/SYSTEM
// transcript, skipping ephemeral thinking/agent_text/tool noise.
func cleanTranscript(turns []*store.ConversationTurn) string {
	var b strings.Builder
	for _, t := range turns {
		switch t.Role {
		case "user":
			b.WriteString("USER: ")
		case "scheduled":
			b.WriteString("SCHEDULED: ")
		case "callback":
			b.WriteString("CALLBACK")
			if t.SourceName != "" {
				b.WriteString(" (")
				b.WriteString(t.SourceName)
				b.WriteString(")")
			}
			b.WriteString(": ")
		case "assistant":
			b.WriteString("ASSISTANT: ")
		case "system", "compaction", "compaction_summary", "distillation", "distillation_partial":
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
func (h *Handler) distillSessionCmd(ctx context.Context, cmd *envelope.Command) *envelope.Response {
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
	capabilities, err := h.gadgetCapabilities(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("read memory capabilities: %v", err))
	}
	if !capabilities.Memory {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest,
			"memory distillation is unavailable: session memory capability is disabled")
	}
	if sess.Status != store.StateIdle {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotIdle, fmt.Sprintf("session is not idle: %s", sess.Status))
	}

	claimed, err := h.store.ClaimIdleSession(ctx, data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to update status: %v", err))
	}
	if !claimed {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotIdle, "session is no longer idle")
	}
	if h.notifyWriter != nil {
		_ = h.notifyWriter.SendAsync(envelope.EventChatStatus, data.SessionID, map[string]string{
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
	reason := "distillation_failed"
	outcome := "distillation_failed"
	defer func() {
		if err := h.store.AddConversationTurn(sessionID, "system", outcome); err != nil {
			h.vlog("distillation: cannot record outcome for session %s: %v", sessionID, err)
		}
		if err := h.store.UpdateSessionStatus(sessionID, store.StateIdle); err != nil {
			h.vlog("distillation: cannot restore idle state for session %s: %v", sessionID, err)
		}
		if h.notifyWriter != nil {
			for _, event := range []string{envelope.EventSessionStateChanged, envelope.EventChatStatus} {
				if err := h.notifyWriter.SendAsync(event, sessionID, map[string]string{"status": store.StateIdle, "reason": reason}); err != nil {
					h.vlog("distillation: cannot notify idle state for session %s: %v", sessionID, err)
				}
			}
		}
	}()

	sess, err := h.store.GetSession(sessionID)
	if err != nil || sess == nil {
		h.vlog("distillation: cannot load session %s: %v", sessionID, err)
		return
	}
	capabilities, err := h.gadgetCapabilities(sessionID)
	if err != nil {
		h.vlog("distillation: cannot read memory capabilities for session %s: %v", sessionID, err)
		return
	}
	if !capabilities.Memory {
		h.vlog("distillation: memory is disabled for session %s", sessionID)
		return
	}

	turns, err := h.store.GetConversation(sessionID)
	if err != nil {
		h.vlog("distillation: cannot load conversation for session %s: %v", sessionID, err)
		return
	}
	// Capture a stable transcript watermark before chunking. Turns appended
	// while auxiliary agents run belong to a later operation, never this
	// snapshot.
	snapshotMaxID := distillationSnapshotMaxID(turns)
	chunks := distillationChunks(turnsThroughSnapshot(turns, snapshotMaxID), maxDistillationChunkBytes)
	if len(chunks) == 0 {
		h.vlog("distillation: nothing to distill for session %s (empty transcript)", sessionID)
		reason = "distilled"
		outcome = "distillation_completed"
		return
	}
	configKey := fmt.Sprintf("%s|%s|%s|%s|%t", sess.Agent, sess.Model, sess.Effort, sess.ContextTier, sess.Yolo)
	progress, err := h.store.GetDistillationProgress(sessionID, snapshotMaxID, configKey)
	if err != nil {
		h.vlog("distillation: cannot load progress for session %s", sessionID)
		return
	}
	start := 0
	if progress != nil {
		start = progress.NextChunk
		if progress.ChunkCount != len(chunks) {
			start = 0
		}
	}
	if err := h.store.PutDistillationProgress(store.DistillationProgress{
		SessionID: sessionID, SnapshotMaxTurnID: snapshotMaxID, ConfigKey: configKey,
		NextChunk: start, ChunkCount: len(chunks),
	}); err != nil {
		h.vlog("distillation: cannot persist progress for session %s", sessionID)
		return
	}

	// Run the agent with the session's system prompt and shared gadget memory
	// contract. Use a throwaway underlying agent session id
	// (NOT persisted) so the live agent session is untouched.
	for i := start; i < len(chunks); i++ {
		chunk := chunks[i]
		environment, envErr := sessionEnvironment(sess)
		if envErr != nil {
			h.vlog("distillation: invalid session environment for %s", sessionID)
			return
		}
		params := agent.RunParams{
			SessionID: sessionID, Agent: sess.Agent,
			Prompt:       fmt.Sprintf(distillPrompt, chunk),
			SystemPrompt: sess.SystemPrompt, Model: sess.Model, Effort: sess.Effort,
			ContextTier: sess.ContextTier, Yolo: sess.Yolo, Path: sess.Path,
			Resume: false, AgentSessionID: newAgentSessionID(),
			SessionDir: h.sessionDir(sessionID), NoPersistTurns: true, Environment: environment,
		}
		if len(params.Prompt) > maxDistillationChunkBytes {
			h.vlog("distillation: prompt exceeds configured cap for session %s", sessionID)
			outcome = "distillation_partial_failed"
			return
		}
		if err := h.prepareRunInstructions(sessionID, &params); err != nil {
			h.vlog("distillation: cannot prepare memory for session %s: %v", sessionID, err)
			outcome = "distillation_partial_failed"
			return
		}
		if _, runErr := h.runAuxiliary(context.Background(), params); runErr != nil {
			h.vlog("distillation agent failed for session %s: %v", sessionID, runErr)
			outcome = "distillation_partial_failed"
			return
		}
		if err := h.store.PutDistillationProgress(store.DistillationProgress{
			SessionID: sessionID, SnapshotMaxTurnID: snapshotMaxID, ConfigKey: configKey,
			NextChunk: i + 1, ChunkCount: len(chunks),
		}); err != nil {
			h.vlog("distillation: cannot persist progress for session %s: %v", sessionID, err)
			outcome = "distillation_partial_failed"
			return
		}
	}
	if err := h.store.ClearDistillationProgress(sessionID, snapshotMaxID, configKey); err != nil {
		h.vlog("distillation: cannot clear progress for session %s: %v", sessionID, err)
		return
	}
	reason = "distilled"
	outcome = "distillation_completed"
	h.vlog("distillation completed for session %s", sessionID)
}
