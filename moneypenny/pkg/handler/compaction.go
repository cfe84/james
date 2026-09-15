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

// compactionDistillPrompt asks the live agent to persist its working context
// into hierarchical memory and then emit a standalone handoff summary as its
// final message. It runs against the CURRENT underlying agent session so all
// of the agent's accumulated context is available.
const compactionDistillPrompt = `[SYSTEM: CONTEXT COMPACTION]
Your context is getting large and is about to be compacted into a fresh session. Do the following, in order:

1. Preserve the durable knowledge and working state from the current conversation according to the system memory contract.

2. Output a comprehensive handoff summary as your FINAL message: original task, key decisions and rationale, important context, current state, and pending actions. Include enough context to resume work, with references to any details you actually saved. Output ONLY the summary text — no preamble or meta-commentary.`

func compactionTaskPrompt(memoryEnabled bool) string {
	if memoryEnabled {
		return compactionDistillPrompt
	}
	return `[SYSTEM: CONTEXT COMPACTION]
Your context is getting large and is about to be compacted into a fresh session. Persistent memory is disabled for this run; do not read or write session memory through gadgets, native file tools, or direct database access.

Output a standalone, comprehensive handoff summary of the available conversation as your FINAL message. The fresh session must be able to resume from this summary alone: include the original task, key decisions and rationale, important context (file paths, names, conventions, learnings), current state, and pending actions. Do not assume access to earlier history or external notes. Output ONLY the summary text — no preamble or meta-commentary.`
}

func compactionSeedSystemPrompt(systemPrompt, summary string) string {
	if summary != "" {
		systemPrompt += "\n\n<prior-session-summary>\n" + summary + "\n</prior-session-summary>"
	}
	return systemPrompt
}

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
	threshold := envelope.EffectiveCompactionThresholdTokens(sess.CompactionThresholdTokens, sess.ContextTier)
	return sess.ContextTokens >= threshold
}

// compactSessionCmd is the dispatch for compact_session: it kicks off the full
// custom-compaction pipeline regardless of the session's configured mode.
func (h *Handler) compactSessionCmd(ctx context.Context, cmd *envelope.Command) *envelope.Response {
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
			"reason": "compacting",
		})
	}

	go h.runCompactionWithParams(data.SessionID, "", sess.Model, sess.Effort, compactionManual, agent.RunParams{})

	return envelope.SuccessResponse(cmd.RequestID, envelope.CompactSessionResponse{SessionID: data.SessionID})
}

// runCompaction runs the full custom-compaction pipeline synchronously:
//
//  1. Distillation + summary (in-session): the live agent reorganizes/saves its
//     working context into hierarchical memory and emits a handoff summary.
//  2. Substitution: a fresh underlying agent session (new agent_session_id, same
//     James session) is started, seeded with the summary and the given next
//     prompt. Memory is used only when enabled by the agent's permissions.
//
// nextPrompt is the prompt to seed the fresh session with for automatic
// compaction. effModel/effEffort are the resolved overrides for the run.
//
// The caller must have set the session status to working.
func (h *Handler) runCompaction(sessionID, nextPrompt, effModel, effEffort string) {
	h.runCompactionWithParams(sessionID, nextPrompt, effModel, effEffort, compactionContinue, agent.RunParams{})
}

type compactionRunMode uint8

const (
	compactionManual compactionRunMode = iota
	compactionContinue
)

// runCompactionWithParams preserves continuation-only metadata (attachments,
// channel routing, and ready markers) while replacing the underlying session.
func (h *Handler) runCompactionWithParams(sessionID, nextPrompt, effModel, effEffort string, mode compactionRunMode, continuation agent.RunParams) {
	notifyIdle := true
	idleReason := "compaction_failed"
	defer func() {
		if err := h.store.UpdateSessionStatus(sessionID, store.StateIdle); err != nil {
			h.vlog("compaction: cannot restore idle state for session %s: %v", sessionID, err)
		}
		if h.notifyWriter != nil && notifyIdle {
			for _, event := range []string{envelope.EventSessionStateChanged, envelope.EventChatStatus} {
				if err := h.notifyWriter.SendAsync(event, sessionID, map[string]string{"status": store.StateIdle, "reason": idleReason}); err != nil {
					h.vlog("compaction: cannot notify idle state for session %s: %v", sessionID, err)
				}
			}
		}
	}()
	sess, err := h.store.GetSession(sessionID)
	if err != nil || sess == nil {
		h.vlog("compaction: cannot load session %s: %v", sessionID, err)
		return
	}
	capabilities, err := h.gadgetCapabilities(sessionID)
	if err != nil {
		h.vlog("compaction: cannot read memory capabilities for session %s: %v", sessionID, err)
		return
	}

	// 1. In-session distillation + handoff summary against the CURRENT
	// underlying agent session, so all of its context is available.
	distillParams := agent.RunParams{
		SessionID:      sessionID,
		Agent:          sess.Agent,
		Prompt:         compactionTaskPrompt(capabilities.Memory),
		SystemPrompt:   sess.SystemPrompt,
		Model:          effModel,
		Effort:         effEffort,
		ContextTier:    sess.ContextTier,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Resume:         true,
		AgentSessionID: sess.AgentSessionID,
		SessionDir:     h.sessionDir(sessionID),
		NoPersistTurns: true,
	}
	if err := h.prepareRunInstructions(sessionID, &distillParams); err != nil {
		h.vlog("compaction: cannot prepare memory for session %s: %v", sessionID, err)
		h.recordCompactionFailure(sessionID, "compaction_prepare_failed")
		return
	}

	// The marker starts the compacted context; do not insert it if preparation fails.
	ctx := context.Background()
	var summary string
	if res, runErr := h.runAuxiliary(ctx, distillParams); runErr != nil {
		h.vlog("compaction distillation failed for session %s: %v", sessionID, runErr)
		h.recordCompactionFailure(sessionID, "compaction_summary_runner_failed")
		return
	} else {
		summary = strings.TrimSpace(res.Text)
	}
	if !validCompactionSummary(summary) {
		h.recordCompactionFailure(sessionID, "compaction_summary_empty")
		return
	}

	// 2. Bootstrap and substitution: create a fresh underlying session without
	// publishing it, then commit the handoff before the real continuation.
	newAgentID := initialAgentSessionID(sess.Agent, newAgentSessionID())
	window := sess.ContextWindow
	if window <= 0 {
		window = contextWindowFor(sess.Agent, effModel)
	}

	seedSystem := compactionSeedSystemPrompt(sess.SystemPrompt, summary)

	seedPrompt := strings.TrimSpace(nextPrompt)
	contextTier := sess.ContextTier
	if continuation.ContextTier != "" {
		contextTier = continuation.ContextTier
	}

	environment, envErr := sessionEnvironment(sess)
	if envErr != nil {
		h.vlog("compaction: invalid session environment for %s", sessionID)
		h.recordCompactionFailure(sessionID, "compaction_environment_failed")
		return
	}
	// Bootstrap creates the new underlying session but is deliberately
	// side-effect free: no turns, schedules, attachments, channel routing, or
	// ready marker. It is prompt-disciplined rather than capability-isolated:
	// the runner has no bootstrap-specific attachment/routing/ready inputs, but
	// the agent process still receives its normal project environment.
	bootstrap := agent.RunParams{
		SessionID:      sessionID,
		Agent:          sess.Agent,
		SystemPrompt:   seedSystem,
		Model:          effModel,
		Effort:         effEffort,
		ContextTier:    contextTier,
		Yolo:           false,
		Path:           sess.Path,
		Resume:         false,
		AgentSessionID: newAgentID,
		SessionDir:     h.sessionDir(sessionID),
		NoPersistTurns: true,
		Environment:    environment,
		Prompt:         "Initialize this session for the supplied handoff; do not produce a user-facing response.",
	}
	// Do not install memory/tool instructions for bootstrap. The handoff
	// summary is already in the seed system prompt; memory/tool setup belongs
	// to the real continuation.
	bootstrapResult, err := h.runAuxiliary(context.Background(), bootstrap)
	if err != nil {
		h.vlog("compaction: bootstrap runner failed for %s", sessionID)
		h.recordCompactionFailure(sessionID, "compaction_bootstrap_failed")
		return
	}
	resolvedAgentID := resolveCompactionAgentSessionID(newAgentID, bootstrapResult)
	if resolvedAgentID == "" {
		h.vlog("compaction: bootstrap returned no agent session id for %s", sessionID)
		h.recordCompactionFailure(sessionID, "compaction_bootstrap_missing_id")
		return
	}
	if err := h.store.CommitCompactionHandoff(sessionID, resolvedAgentID, window, summary); err != nil {
		h.vlog("compaction: handoff commit failed for %s", sessionID)
		h.recordCompactionFailure(sessionID, "compaction_handoff_commit_failed")
		return
	}
	idleReason = "compacted"
	// Manual compaction stops here: it must not manufacture an assistant turn.
	if mode == compactionManual {
		return
	}
	// The continuation owns its completion/error notifications after the
	// handoff. Do not emit a second, misleading compaction result.
	notifyIdle = false
	// The real continuation resumes the bootstrapped session and carries all
	// routing/attachment metadata from the original request.
	h.runAgent(sessionID, agent.RunParams{
		SessionID: sessionID, Agent: sess.Agent, Prompt: seedPrompt,
		SystemPrompt: seedSystem, Model: effModel, Effort: effEffort,
		ContextTier: contextTier, Yolo: sess.Yolo, Path: sess.Path,
		Resume: true, AgentSessionID: resolvedAgentID, Attachments: continuation.Attachments,
		ReplyChannelID: continuation.ReplyChannelID, MarkReady: continuation.MarkReady,
		Environment: environment,
	})
}

func resolveCompactionAgentSessionID(requested string, result *agent.Result) string {
	if result != nil {
		if providerID := strings.TrimSpace(result.AgentSessionID); providerID != "" {
			return providerID
		}
	}
	return strings.TrimSpace(requested)
}

func validCompactionSummary(summary string) bool {
	return strings.TrimSpace(summary) != ""
}

func (h *Handler) recordCompactionFailure(sessionID, message string) {
	if err := h.store.AddConversationTurn(sessionID, "system", message); err != nil {
		h.vlog("compaction: cannot record failure for session %s: %v", sessionID, err)
	}
}
