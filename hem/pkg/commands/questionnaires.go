package commands

import (
	"context"
	"encoding/json"

	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/questionnaire"
)

func (e *Executor) resolveQuestionnaire(args []string) *protocol.Response {
	if len(args) != 1 {
		return protocol.ErrResponse("resolve questionnaire requires one JSON request")
	}
	var request questionnaire.Resolution
	if err := decodeGadgetData([]byte(args[0]), &request); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	session, err := e.store.GetSession(request.SessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if session == nil {
		return protocol.ErrResponse("questionnaire session is not tracked by this Hem")
	}
	mp, err := e.store.GetMoneypenny(session.MoneypennyName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse("questionnaire host is no longer registered")
	}
	resp, err := e.sendCommand(context.Background(), mp, "resolve_questionnaire", request)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	e.invalidateMPCache(mp.Name)
	return protocol.OKResponse(json.RawMessage(resp.Data))
}
