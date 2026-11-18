package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/artifact"
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
	session, err := e.store.GetSession(request.SessionID)
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
	resp, err := e.sendCommand(context.Background(), mp, "read_artifact", request)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	var chunk artifact.Chunk
	if err := json.Unmarshal(resp.Data, &chunk); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("decoding artifact: %v", err))
	}
	return protocol.OKResponse(chunk)
}
