package handler

import (
	"context"
	"fmt"
	"strings"

	"james/moneypenny/pkg/envelope"
)

func (h *Handler) setAttention(sessionID, kind, reason, source string) (envelope.SessionReport, bool, error) {
	return h.store.SetSessionReport(sessionID, envelope.AttentionUpdate{Kind: &kind, Reason: reason}, source)
}

func (h *Handler) notifyAttention(sessionID, message string) error {
	_, changed, err := h.setAttention(sessionID, "needs_action", message, "agent")
	if err != nil {
		return err
	}
	if changed {
		return h.store.AddConversationTurn(sessionID, "notification", message)
	}
	return nil
}

func (h *Handler) extractAttentionTags(sessionID, content string) string {
	for _, match := range notifyUserTagRe.FindAllStringSubmatch(content, -1) {
		if message := strings.TrimSpace(match[1]); message != "" {
			if err := h.notifyAttention(sessionID, message); err != nil {
				h.errorLog("failed to persist attention: session=%s error=%v", sessionID, err)
			}
		}
	}
	return notifyUserTagRe.ReplaceAllString(content, "")
}

func (h *Handler) attentionGadget(sessionID string, request gadgetRequest) (any, error) {
	if request.Method == "attention.get" {
		var data struct{}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		session, err := h.store.GetSession(sessionID)
		if err != nil {
			return nil, err
		}
		if session == nil {
			return nil, fmt.Errorf("session not found")
		}
		return session.SessionReport, nil
	}
	var update envelope.AttentionUpdate
	if err := decodeGadget(request.Data, &update); err != nil {
		return nil, err
	}
	if update.SessionID != "" {
		return nil, &gadgetError{"invalid_request", "Attention is scoped to the authenticated session"}
	}
	if err := update.Validate(); err != nil {
		return nil, &gadgetError{"invalid_request", err.Error()}
	}
	if update.Kind != nil && *update.Kind == "failed" {
		return nil, &gadgetError{"invalid_request", "Execution failures are reported by Moneypenny"}
	}
	report, _, err := h.store.SetSessionReport(sessionID, update, "agent")
	return report, err
}

func (h *Handler) updateAttention(_ context.Context, cmd *envelope.Command) *envelope.Response {
	var update envelope.AttentionUpdate
	if err := decodeGadget(cmd.Data, &update); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	if update.SessionID == "" {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id required")
	}
	if err := update.Validate(); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	report, _, err := h.store.SetSessionReport(update.SessionID, update, "operator")
	if err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(cmd.RequestID, report)
}
