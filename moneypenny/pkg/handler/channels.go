package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/channel"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

// channelInfo converts a stored channel to its envelope representation.
func channelInfo(ch *store.Channel) envelope.ChannelInfo {
	info := envelope.ChannelInfo{
		ID:          ch.ID,
		SessionID:   ch.SessionID,
		Provider:    ch.Provider,
		TargetID:    ch.TargetID,
		TargetLabel: ch.TargetLabel,
		Enabled:     ch.Enabled,
		Mention:     ch.Mention,
		AllowAnyone: ch.AllowAnyone,
		LastError:   ch.LastError,
		CreatedAt:   ch.CreatedAt.UTC().Format(time.RFC3339),
	}
	if !ch.LastActivity.IsZero() {
		info.LastActivity = ch.LastActivity.UTC().Format(time.RFC3339)
	}
	return info
}

func (h *Handler) listChannelProviders(_ context.Context, cmd *envelope.Command) *envelope.Response {
	metas := h.channels.List()
	providers := make([]envelope.ChannelProviderInfo, 0, len(metas))
	for _, m := range metas {
		providers = append(providers, envelope.ChannelProviderInfo{
			Name:      m.Name,
			Search:    m.Caps.Search,
			ProvideID: m.Caps.ProvideID,
		})
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.ListChannelProvidersResponse{Providers: providers})
}

func (h *Handler) searchChannelTargets(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SearchChannelTargetsData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.Provider == "" || strings.TrimSpace(data.Query) == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "provider and query are required")
	}
	prov, err := h.channels.Get(data.Provider)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	if !prov.Caps().Search {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("provider %q does not support search", data.Provider))
	}
	targets, err := prov.Search(ctx, data.Query)
	if err != nil {
		// Surface provider/auth errors verbatim so the user can act (authenticate).
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	out := make([]envelope.ChannelTargetInfo, 0, len(targets))
	for _, t := range targets {
		out = append(out, envelope.ChannelTargetInfo{ID: t.ID, Label: t.Label, Detail: t.Detail})
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.SearchChannelTargetsResponse{Targets: out})
}

func (h *Handler) createChannel(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.CreateChannelData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.SessionID == "" || data.Provider == "" || strings.TrimSpace(data.TargetID) == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id, provider, and target_id are required")
	}
	if sess, err := h.store.GetSession(data.SessionID); err != nil || sess == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrSessionNotFound, fmt.Sprintf("session not found: %s", data.SessionID))
	}
	prov, err := h.channels.Get(data.Provider)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}

	label := data.TargetLabel
	// Initialize the poll cursor at the target's latest message so history is
	// not replayed. Best-effort: on error we still bind (cursor empty = only
	// strictly-future messages forwarded once a first message sets the cursor).
	var lastSeenID, lastSeenTS string
	if tp, ok := prov.(interface {
		LatestCursor(context.Context, string) (string, string, error)
	}); ok {
		if id, ts, cerr := tp.LatestCursor(ctx, data.TargetID); cerr == nil {
			lastSeenID, lastSeenTS = id, ts
		} else {
			h.vlog("create_channel: LatestCursor failed for %s: %v", data.TargetID, cerr)
		}
	}
	if label == "" {
		if t, rerr := prov.Resolve(ctx, data.TargetID); rerr == nil {
			label = t.Label
		}
	}
	if label == "" {
		label = data.TargetID
	}

	ch := &store.Channel{
		SessionID:   data.SessionID,
		Provider:    data.Provider,
		TargetID:    data.TargetID,
		TargetLabel: label,
		Enabled:     true,
		Mention:     channel.NormalizeMention(data.Mention),
		AllowAnyone: data.AllowAnyone,
		LastSeenID:  lastSeenID,
		LastSeenTS:  lastSeenTS,
	}
	id, err := h.store.CreateChannel(ch)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to create channel: %v", err))
	}
	ch.ID = id
	stored, _ := h.store.GetChannel(id)
	if stored != nil {
		ch = stored
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.CreateChannelResponse{Channel: channelInfo(ch)})
}

func (h *Handler) listChannels(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ListChannelsData
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &data); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
		}
	}
	chans, err := h.store.ListChannels(data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, fmt.Sprintf("failed to list channels: %v", err))
	}
	out := make([]envelope.ChannelInfo, 0, len(chans))
	for _, ch := range chans {
		out = append(out, channelInfo(ch))
	}
	return envelope.SuccessResponse(cmd.RequestID, envelope.ListChannelsResponse{Channels: out})
}

func (h *Handler) deleteChannel(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.ChannelIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if err := h.store.DeleteChannel(data.ChannelID); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, map[string]bool{"deleted": true})
}

func (h *Handler) setChannelEnabled(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SetChannelEnabledData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if err := h.store.SetChannelEnabled(data.ChannelID, data.Enabled); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	ch, _ := h.store.GetChannel(data.ChannelID)
	if ch == nil {
		return envelope.SuccessResponse(cmd.RequestID, map[string]bool{"enabled": data.Enabled})
	}
	return envelope.SuccessResponse(cmd.RequestID, channelInfo(ch))
}

func (h *Handler) updateChannel(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.UpdateChannelData
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("invalid data: %v", err))
	}
	if data.ChannelID == 0 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "channel_id is required")
	}
	if data.Mention != nil {
		if err := h.store.UpdateChannelMention(data.ChannelID, channel.NormalizeMention(*data.Mention)); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
		}
	}
	if data.AllowAnyone != nil {
		if err := h.store.SetChannelAllowAnyone(data.ChannelID, *data.AllowAnyone); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
		}
	}
	ch, _ := h.store.GetChannel(data.ChannelID)
	if ch == nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, fmt.Sprintf("channel %d not found", data.ChannelID))
	}
	return envelope.SuccessResponse(cmd.RequestID, channelInfo(ch))
}

// Poll cadence: fast while a channel is active (recent traffic), slow otherwise.
const (
	channelTick        = 5 * time.Second
	channelPollFast    = 10 * time.Second
	channelPollSlow    = 60 * time.Second
	channelActiveAfter = 5 * time.Minute
)

// StartChannelManager launches the background loop that polls channels for
// inbound messages and drains the outbox to send replies. It is a no-op-safe
// analog of StartScheduler and should be started right after it.
func (h *Handler) StartChannelManager(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(channelTick)
		defer ticker.Stop()
		lastPolled := map[int64]time.Time{}
		for {
			select {
			case <-ctx.Done():
				h.channels.Close()
				return
			case <-ticker.C:
				h.pollChannels(ctx, lastPolled)
				h.drainOutbox(ctx)
			}
		}
	}()
}

func (h *Handler) pollChannels(ctx context.Context, lastPolled map[int64]time.Time) {
	chans, err := h.store.ListEnabledChannels()
	if err != nil {
		h.vlog("channel: list enabled failed: %v", err)
		return
	}
	now := time.Now()
	for _, ch := range chans {
		cadence := channelPollSlow
		if !ch.LastActivity.IsZero() && now.Sub(ch.LastActivity) < channelActiveAfter {
			cadence = channelPollFast
		}
		if lp, ok := lastPolled[ch.ID]; ok && now.Sub(lp) < cadence {
			continue
		}
		lastPolled[ch.ID] = now
		h.pollChannel(ctx, ch)
	}
}

func (h *Handler) pollChannel(ctx context.Context, ch *store.Channel) {
	prov, err := h.channels.Get(ch.Provider)
	if err != nil {
		h.vlog("channel %d: provider error: %v", ch.ID, err)
		return
	}

	// Bootstrap an empty cursor to the target's latest message. A provider's
	// ListMessages returns nothing for an empty sinceTS (to avoid replaying
	// history), so without this a freshly-bound channel — or one whose bind-time
	// LatestCursor failed (e.g. auth not yet ready) — would stay permanently dead.
	if ch.LastSeenTS == "" {
		id, ts, err := prov.LatestCursor(ctx, ch.TargetID)
		if err != nil {
			_ = h.store.SetChannelError(ch.ID, err.Error())
			return
		}
		_ = h.store.SetChannelError(ch.ID, "")
		if ts != "" {
			_ = h.store.UpdateChannelCursor(ch.ID, id, ts)
		}
		return
	}

	msgs, err := prov.ListMessages(ctx, ch.TargetID, ch.LastSeenTS)
	if err != nil {
		_ = h.store.SetChannelError(ch.ID, err.Error())
		return
	}
	_ = h.store.SetChannelError(ch.ID, "")
	if len(msgs) == 0 {
		return
	}

	// Resolve the owner's identity once when the channel restricts to the
	// signed-in account. On lookup failure we fail open (forward everything) but
	// surface the error so the operator can see gating is degraded, rather than
	// silently muting the channel.
	var selfID string
	if !ch.AllowAnyone {
		if id, _, serr := prov.Self(ctx); serr != nil {
			_ = h.store.SetChannelError(ch.ID, fmt.Sprintf("owner identity lookup failed (forwarding all): %v", serr))
		} else {
			selfID = id
		}
	}

	sent, _ := h.store.SentMessageIDs(ch.ID)
	selfPrefix := strings.TrimRight(h.prefixForChannel(ch.ID), "\n")
	newestID, newestTS := ch.LastSeenID, ch.LastSeenTS
	var inbound []channel.Message
	for _, m := range msgs {
		if m.Timestamp > newestTS {
			newestTS, newestID = m.Timestamp, m.ID
		}
		if sent[m.ID] {
			continue
		}
		if selfPrefix != "" && strings.HasPrefix(strings.TrimSpace(m.Text), selfPrefix) {
			continue
		}
		// Sender gate: unless the channel allows anyone (or the owner is
		// unknown), only forward messages from the signed-in owner. Non-matching
		// messages are examined and intentionally ignored, so the cursor still
		// advances past them.
		if !ch.AllowAnyone && selfID != "" && m.SenderID != selfID {
			continue
		}
		// Mention gate: when configured, only forward messages addressing the
		// mention, and strip the mention token from the forwarded text.
		if ch.Mention != "" {
			stripped, ok := channel.MatchAndStripMention(m.Text, ch.Mention)
			if !ok {
				continue
			}
			// A message that is only the mention (nothing left after stripping)
			// carries no instruction — skip it (cursor still advances).
			if strings.TrimSpace(stripped) == "" {
				continue
			}
			m.Text = stripped
		}
		inbound = append(inbound, m)
	}

	advance := func() {
		if newestTS != ch.LastSeenTS || newestID != ch.LastSeenID {
			_ = h.store.UpdateChannelCursor(ch.ID, newestID, newestTS)
		}
	}

	if len(inbound) == 0 {
		// Only our own messages; safe to skip past them.
		advance()
		return
	}
	_ = h.store.TouchChannelActivity(ch.ID)
	// Advance the cursor only once the inbound messages have been handed off, so
	// a transient forward failure (e.g. queue insert error) retries next poll
	// instead of silently dropping them.
	if h.forwardInbound(ch, inbound) {
		advance()
	}
}

// forwardInbound composes the inbound messages into a prompt and hands it to the
// session (running it if idle, queueing it if busy) with the reply routed back
// to the originating channel. It returns true when the cursor may be advanced —
// i.e. the messages were handed off, or are unrecoverable (dropping them avoids
// infinite reprocessing) — and false on a transient failure worth retrying.
func (h *Handler) forwardInbound(ch *store.Channel, msgs []channel.Message) bool {
	sess, err := h.store.GetSession(ch.SessionID)
	if err != nil || sess == nil {
		h.vlog("channel %d: session %s not found", ch.ID, ch.SessionID)
		return true // orphaned channel; retrying can never succeed
	}

	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		sender := m.SenderName
		if sender == "" {
			sender = m.SenderID
		}
		if sender != "" {
			b.WriteString(sender)
			b.WriteString(": ")
		}
		b.WriteString(m.Text)
	}
	prompt := strings.TrimSpace(b.String())
	if prompt == "" {
		return true
	}

	if sess.Status != store.StateIdle {
		if err := h.store.QueuePromptChannel(ch.SessionID, prompt, "", "", "", "channel", ch.ID); err != nil {
			h.vlog("channel %d: queue failed: %v", ch.ID, err)
			return false
		}
		return true
	}

	if err := h.store.UpdateSessionStatus(ch.SessionID, store.StateWorking); err != nil {
		h.vlog("channel %d: set working failed: %v", ch.ID, err)
		return false
	}
	_ = h.store.AddConversationTurn(ch.SessionID, "user", prompt)
	if h.notifyWriter != nil {
		_ = h.notifyWriter.Send(envelope.EventChatStatus, ch.SessionID, map[string]string{
			"status": store.StateWorking,
			"reason": "channel_message",
		})
		_ = h.notifyWriter.Send(envelope.EventSessionStateChanged, ch.SessionID, map[string]string{
			"status": store.StateWorking,
			"reason": "channel_message",
		})
	}
	go h.runAgent(ch.SessionID, agent.RunParams{
		SessionID:      ch.SessionID,
		Agent:          sess.Agent,
		Prompt:         prompt,
		SystemPrompt:   sess.SystemPrompt,
		Model:          sess.Model,
		Effort:         sess.Effort,
		ContextTier:    sess.ContextTier,
		Yolo:           sess.Yolo,
		Path:           sess.Path,
		Resume:         true,
		ReplyChannelID: ch.ID,
	})
	return true
}

// drainOutbox sends pending outbound messages, prefixing each with the sending
// session's name so recipients can tell which agent is speaking.
func (h *Handler) drainOutbox(ctx context.Context) {
	items, err := h.store.PendingOutbox()
	if err != nil {
		h.vlog("channel: pending outbox failed: %v", err)
		return
	}
	for _, it := range items {
		prov, err := h.channels.Get(it.Provider)
		if err != nil {
			_ = h.store.MarkOutboxError(it.ID, err.Error())
			continue
		}
		content := h.prefixForChannel(it.ChannelID) + it.Content
		msgID, err := prov.Send(ctx, it.TargetID, content)
		if err != nil {
			_ = h.store.MarkOutboxError(it.ID, err.Error())
			_ = h.store.SetChannelError(it.ChannelID, err.Error())
			continue
		}
		_ = h.store.MarkOutboxSent(it.ID, msgID)
		_ = h.store.SetChannelError(it.ChannelID, "")
		_ = h.store.TouchChannelActivity(it.ChannelID)
	}
}

// prefixForChannel returns the "[<session name>]\n" prefix for a channel's
// outbound messages, falling back to the agent type and finally a generic tag.
func (h *Handler) prefixForChannel(channelID int64) string {
	ch, err := h.store.GetChannel(channelID)
	if err != nil || ch == nil {
		return ""
	}
	name := ""
	if sess, err := h.store.GetSession(ch.SessionID); err == nil && sess != nil {
		name = sess.Name
		if name == "" {
			name = sess.Agent
		}
	}
	if name == "" {
		name = "agent"
	}
	return "[" + name + "]\n"
}
