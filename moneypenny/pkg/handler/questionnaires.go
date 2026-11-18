package handler

import (
	"encoding/json"
	"errors"
	"strings"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/questionnaire"
	"james/moneypenny/pkg/store"
)

func (h *Handler) publishQuestionnaire(sessionID string, raw json.RawMessage) (any, error) {
	var definition questionnaire.Definition
	if err := decodeGadget(raw, &definition); err != nil {
		return nil, &gadgetError{"invalid_request", err.Error()}
	}
	if err := definition.Validate(); err != nil {
		return nil, &gadgetError{"invalid_request", err.Error()}
	}
	if err := h.store.PublishQuestionnaire(sessionID, definition); err != nil {
		if errors.Is(err, store.ErrQuestionnaireInvalid) {
			return nil, &gadgetError{"invalid_request", err.Error()}
		}
		return nil, err
	}
	return map[string]string{"title": definition.Title}, nil
}

func (h *Handler) resolveQuestionnaire(cmd *envelope.Command) *envelope.Response {
	var request questionnaire.Resolution
	if err := decodeGadget(cmd.Data, &request); err != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	if strings.TrimSpace(request.SessionID) == "" || request.ID <= 0 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session_id and a positive questionnaire id are required")
	}
	h.runsMu.Lock()
	if h.runsClosing {
		h.runsMu.Unlock()
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "Moneypenny is shutting down")
	}
	claimed, err := h.store.ResolveQuestionnaire(request)
	if claimed {
		h.runsWG.Add(1)
	}
	h.runsMu.Unlock()
	if errors.Is(err, store.ErrQuestionnaireInvalid) {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, err.Error())
	}
	if err != nil {
		h.errorLog("resolve questionnaire %d for session %s: %v", request.ID, request.SessionID, err)
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "failed to resolve questionnaire")
	}
	if claimed {
		go func() {
			defer h.runsWG.Done()
			h.continueQueuedPrompts(request.SessionID)
		}()
	}
	return envelope.SuccessResponse(cmd.RequestID, map[string]string{"action": request.Action})
}
