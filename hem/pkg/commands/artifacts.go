package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

// ReadArtifact relays a bounded chunk from the owning MP without storing it.
func (e *Executor) ReadArtifact(args []string) *protocol.Response {
	if len(args) != 1 {
		return protocol.ErrResponse("read artifact requires one JSON request")
	}
	var request artifact.ReadRequest
	if err := decodeGadgetData([]byte(args[0]), &request); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return e.relayArtifact(request.SessionID, "read_artifact", request)
}

func (e *Executor) artifactGadget(source *store.Session, method string, data json.RawMessage) *protocol.Response {
	caps, err := e.currentGadgetCapabilities(source)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if !caps.Agents {
		return protocol.ErrResponse("all-agent messaging capability is disabled")
	}
	if method == "artifacts.list" {
		var request envelope.SessionIDData
		if err := decodeGadgetData(data, &request); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return e.relayArtifact(request.SessionID, "list_artifacts", request)
	}
	var request artifact.ReadRequest
	if err := decodeGadgetData(data, &request); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	return e.relayArtifact(request.SessionID, "read_artifact", request)
}

func (e *Executor) relayArtifact(sessionID, method string, request any) *protocol.Response {
	session, err := e.store.GetSession(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if session == nil {
		return protocol.ErrResponse("artifact session is not tracked by this Hem")
	}
	mp, err := e.store.GetMoneypenny(session.MoneypennyName)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse("artifact host is no longer registered")
	}
	resp, err := e.sendCommand(context.Background(), mp, method, request)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if !json.Valid(resp.Data) {
		return protocol.ErrResponse(fmt.Sprintf("invalid response from %s", method))
	}
	return protocol.OKResponse(resp.Data)
}
