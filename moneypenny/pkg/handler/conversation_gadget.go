package handler

import (
	"context"
	"time"

	"james/moneypenny/pkg/envelope"
)

func (h *Handler) conversationGadget(ctx context.Context, caller string, allowPeers bool, request gadgetRequest) (any, error) {
	var q envelope.ConversationQuery
	if err := decodeGadget(request.Data, &q); err != nil {
		return nil, err
	}
	search := request.Method == "conversation.search"
	if q.SessionID == "" || q.SessionID == caller {
		q.SessionID = caller
		return h.runConversationQuery(ctx, q, search, caller)
	}
	if !allowPeers {
		return nil, &gadgetError{"permission_denied", "reading other agents' conversations is disabled"}
	}
	if !sessionIDPattern.MatchString(q.SessionID) {
		return nil, &gadgetError{"invalid_request", "session_id must be a canonical UUID"}
	}
	return h.routeGadget(ctx, caller, request.Method, request.Data)
}

func (h *Handler) runConversationQuery(ctx context.Context, q envelope.ConversationQuery, search bool, caller string) (envelope.ConversationPage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return h.store.QueryConversation(ctx, q, search, caller)
}

func (h *Handler) queryConversation(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var request envelope.RoutedConversationQuery
	if err := decodeGadget(cmd.Data, &request); err != nil ||
		!sessionIDPattern.MatchString(request.Query.SessionID) || !sessionIDPattern.MatchString(request.Caller) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid routed conversation query")
	}
	page, err := h.runConversationQuery(ctx, request.Query, request.Search, request.Caller)
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, page)
}
