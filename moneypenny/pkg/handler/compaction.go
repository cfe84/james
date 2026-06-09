package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

// compactionThreshold is the fraction of a model's context window at which
// custom compaction is triggered before the next turn.
const compactionThreshold = 0.75

// compactionDistillPrompt asks the live agent to persist its working context
// into hierarchical memory and then emit a standalone handoff summary as its
// final message. It runs against the CURRENT underlying agent session so all
// of the agent's accumulated context is available.
const compactionDistillPrompt = `[SYSTEM: CONTEXT COMPACTION]
Your context is getting large and is about to be compacted into a fresh session. Do the following, in order:

1. Review your hierarchical memory (use the 'show memory', 'list memory', and 'search memory' tools to see what's already there). Reorganize it if needed and SAVE everything important from the current conversation into memory using 'update memory <path>': the original task, key decisions and their rationale, important context (file paths, names, conventions, learnings), the current state of the work, and any pending actions. Keep high-level synthesis in parent nodes and detail in child nodes so nothing is lost.

2. After memory is saved, output a comprehensive handoff summary of the current conversation as your FINAL message. It must be detailed enough that the work can be resumed from the summary plus memory alone: original task, key decisions, current state, and pending actions. Output ONLY the summary text as your final message — no preamble or meta-commentary.`

// newAgentSessionID returns a fresh UUID v4 to use as an underlying agent
// session id.
func newAgentSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is not recoverable here; surface it loudly.
		panic(fmt.Sprintf("failed to generate agent session id: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// contextWindowFor returns a burned-in context window (in tokens) for an
// agent/model. Claude reports its own window in the result stream, so this is
// primarily used for Copilot (which exposes no usage) and as a fallback. These
// values are intentionally code-tunable rather than user-configurable; adjust
// here as model context windows change.
func contextWindowFor(agentName, model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "gpt-5"), strings.Contains(m, "gpt5"):
		return 1_000_000
	case strings.Contains(m, "gemini"):
		return 1_000_000
	case strings.Contains(m, "opus"), strings.Contains(m, "sonnet"), strings.Contains(m, "haiku"):
		return 200_000
	}
	// Conservative default when the model is unknown. Copilot's default model
	// family is GPT-5-class, so assume a large window there; otherwise stay low.
	if agentName == "copilot" {
		return 1_000_000
	}
	return 200_000
}

// estimateContextTokens approximates the underlying context size from the
// stored transcript (~4 characters per token) when the agent does not report
// real usage (e.g. Copilot). Only turns since the most recent compaction are
// counted: compaction substitutes a fresh underlying agent session whose
// context starts empty, so counting the whole James transcript would keep the
// estimate permanently over threshold and re-trigger compaction every turn.
func (h *Handler) estimateContextTokens(sessionID string) int {
	turns, err := h.store.GetConversation(sessionID)
	if err != nil {
		return 0
	}
	// Find the last compaction marker; only turns after it belong to the
	// current underlying agent session.
	start := 0
	for i, t := range turns {
		if t.Role == "compaction" {
			start = i + 1
		}
	}
	chars := 0
	for _, t := range turns[start:] {
		chars += len(t.Content)
	}
	return chars / 4
}

// recordContextUsage stores the post-turn context size and the model's context
// window for a session so compaction can be triggered and usage displayed.
func (h *Handler) recordContextUsage(sessionID string, params agent.RunParams, result *agent.Result) {
	if result == nil {
		return
	}
	tokens := result.ContextTokens
	window := result.ContextWindow
	if tokens == 0 {
		tokens = h.estimateContextTokens(sessionID)
	}
	if window == 0 {
		window = contextWindowFor(params.Agent, params.Model)
	}
	if err := h.store.SetContextUsage(sessionID, tokens, window); err != nil {
		h.vlog("failed to record context usage for session %s: %v", sessionID, err)
	}
}

// shouldCompact reports whether custom compaction should run before the next
// turn for this session, based on its mode and last-measured context size.
func (h *Handler) shouldCompact(sess *store.Session) bool {
	if sess == nil || sess.CompactionMode != store.CompactionCustom {
		return false
	}
	window := sess.ContextWindow
	if window <= 0 {
		window = contextWindowFor(sess.Agent, sess.Model)
	}
	if window <= 0 {
		return false
	}
	return sess.ContextTokens >= int(float64(window)*compactionThreshold)
}

// compactSessionCmd is the dispatch for compact_session: it kicks off the full
// custom-compaction pipeline regardless of the session's configured mode.
func (h *Handler) compactSessionCmd(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.CompactSessionData
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
			"reason": "compacting",
		})
	}

	go h.runCompaction(data.SessionID, "Await next instructions.", sess.Model, sess.Effort)

	return envelope.SuccessResponse(cmd.RequestID, envelope.CompactSessionResponse{SessionID: data.SessionID})
}

// runCompaction runs the full custom-compaction pipeline synchronously:
//
//  1. Distillation + summary (in-session): the live agent reorganizes/saves its
//     working context into hierarchical memory and emits a handoff summary.
//  2. Substitution: a fresh underlying agent session (new agent_session_id, same
//     James session) is started, seeded with the summary, a note that memory
//     holds the full detail, and the given next prompt.
//
// nextPrompt is the prompt to seed the fresh session with: the actual next
// prompt for automatic compaction, or "Await next instructions." for manual
// compaction. effModel/effEffort are the resolved overrides for the run.
//
// The caller must have set the session status to working.
func (h *Handler) runCompaction(sessionID, nextPrompt, effModel, effEffort string) {
	sess, err := h.store.GetSession(sessionID)
	if err != nil || sess == nil {
		h.vlog("compaction: cannot load session %s: %v", sessionID, err)
		_ = h.store.UpdateSessionStatus(sessionID, store.StateIdle)
		return
	}

	// Collapsed history marker. The distillation's thinking/agent_text turns
	// (persisted by the runner) provide the chain-of-thought detail shown when
	// train-of-thought is enabled.
	_ = h.store.AddConversationTurn(sessionID, "compaction", "compacted")

	// 1. In-session distillation + handoff summary against the CURRENT
	// underlying agent session, so all of its context is available.
	distillParams := agent.RunParams{
		SessionID:      sessionID,
		Agent:          sess.Agent,
		Prompt:         compactionDistillPrompt,
		SystemPrompt:   sess.SystemPrompt,
		Model:          effModel,
		Effort:         effEffort,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Resume:         true,
		AgentSessionID: sess.AgentSessionID,
		SessionDir:     h.sessionDir(sessionID),
	}
	if outline, err := h.store.MemoryOutline(sessionID); err == nil && outline != "" {
		distillParams.SystemPrompt += "\n\n<session-memory-outline>\n" + outline +
			"\n</session-memory-outline>\n(Use 'show memory <path>' to read a node, 'search memory <query>' to search.)"
	}

	ctx := context.Background()
	var summary string
	if res, runErr := h.runner.Run(ctx, distillParams); runErr != nil {
		h.vlog("compaction distillation failed for session %s: %v", sessionID, runErr)
	} else {
		summary = strings.TrimSpace(res.Text)
	}

	// The runner already persisted the distillation's narration as agent_text
	// train-of-thought turns; the final summary often duplicates the trailing
	// one (Claude). Replace it with a single canonical agent_text turn so the
	// summary is visible when train-of-thought is shown without duplication.
	if summary != "" {
		_, _ = h.store.DeleteLastTurnIfMatches(sessionID, "agent_text", summary)
		_ = h.store.AddConversationTurn(sessionID, "agent_text", summary)
	}

	// 2. Substitution: mint a fresh underlying agent session id and reset the
	// measured context to a clean baseline.
	newAgentID := newAgentSessionID()
	_ = h.store.SetAgentSessionID(sessionID, newAgentID)
	window := sess.ContextWindow
	if window <= 0 {
		window = contextWindowFor(sess.Agent, effModel)
	}
	_ = h.store.SetContextUsage(sessionID, 0, window)

	seedSystem := sess.SystemPrompt
	if summary != "" {
		seedSystem += "\n\n<prior-session-summary>\n" + summary + "\n</prior-session-summary>"
	}
	seedSystem += "\n\n<memory-note>\nYour hierarchical memory contains the full history of important details from before this point. Use 'show memory <path>' and 'search memory <query>' to retrieve specifics when needed.\n</memory-note>"

	seedPrompt := strings.TrimSpace(nextPrompt)
	if seedPrompt == "" {
		seedPrompt = "Await next instructions."
	}

	// Run the fresh session through the normal path so assistant-turn storage,
	// queue draining, context tracking, and idle notification all apply.
	// runAgent appends the current memory outline and uses the new agent id.
	h.runAgent(sessionID, agent.RunParams{
		SessionID:      sessionID,
		Agent:          sess.Agent,
		Prompt:         seedPrompt,
		SystemPrompt:   seedSystem,
		Model:          effModel,
		Effort:         effEffort,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Resume:         false,
		AgentSessionID: newAgentID,
	})
}
