package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

func (h *Handler) exportConversation(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.SessionIDData
	if err := json.Unmarshal(cmd.Data, &data); err != nil || !sessionIDPattern.MatchString(data.SessionID) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id must be a canonical UUID")
	}
	turns, err := h.store.ExportConversation(ctx, data.SessionID)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	history := make([]envelope.ConversationTurn, 0, len(turns))
	for _, turn := range turns {
		history = append(history, envelope.ConversationTurn{
			Role: turn.Role, Content: turn.Content, CreatedAt: turn.CreatedAt.UTC().Format(time.RFC3339Nano),
			SourceSessionID: turn.SourceSessionID, SourceName: turn.SourceName,
		})
	}
	raw, err := json.Marshal(history)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, err.Error())
	}
	if len(raw) > envelope.MaxTransferredHistoryBytes {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "encoded conversation exceeds the 4 MiB cross-host copy limit")
	}
	return envelope.SuccessResponse(cmd.RequestID, json.RawMessage(raw))
}

func importedConversation(raw json.RawMessage) ([]*store.ConversationTurn, error) {
	if len(raw) > envelope.MaxTransferredHistoryBytes {
		return nil, fmt.Errorf("encoded conversation exceeds the 4 MiB cross-host copy limit")
	}
	var history []envelope.ConversationTurn
	if err := json.Unmarshal(raw, &history); err != nil || history == nil {
		return nil, fmt.Errorf("transferred conversation must be an array of turns")
	}
	if len(history) > envelope.MaxTransferredHistoryTurns {
		return nil, fmt.Errorf("conversation exceeds the 10,000-turn cross-host copy limit")
	}
	turns := make([]*store.ConversationTurn, 0, len(history))
	for _, turn := range history {
		at, err := time.Parse(time.RFC3339Nano, turn.CreatedAt)
		if err != nil || at.IsZero() || turn.Role == "" || turn.Role == "artifact" {
			return nil, fmt.Errorf("invalid transferred conversation turn at position %d", len(turns)+1)
		}
		turns = append(turns, &store.ConversationTurn{
			Role: turn.Role, Content: turn.Content, CreatedAt: at.UTC(),
			SourceSessionID: turn.SourceSessionID, SourceName: turn.SourceName,
		})
	}
	return turns, nil
}
