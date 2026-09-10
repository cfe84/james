package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/envelope"
)

func (e *Executor) traitsGadget(source *store.Session, method string, raw json.RawMessage) *protocol.Response {
	data, err := envelope.DecodeTraitGadget(method, raw)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	mp, err := e.store.GetMoneypenny(source.MoneypennyName)
	if err != nil || mp == nil {
		return protocol.ErrResponse("gadget source moneypenny is not registered")
	}
	// Shared definitions require a fresh grant, not cached dashboard metadata
	// or permissions asserted in the routed request.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	detail, err := e.sendCommand(ctx, mp, "get_session", map[string]interface{}{"session_id": source.SessionID})
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("getting trait permissions: %v", err))
	}
	if detail.Status != envelope.StatusSuccess {
		return protocol.ErrResponse("invalid response while getting trait permissions")
	}
	caps, err := sessionGadgetCapabilities(detail.Data)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if !caps.Traits {
		return protocol.ErrResponse("traits capability is disabled")
	}
	if method == "traits.list" {
		return e.ListTraits(nil)
	}
	trait, err := e.store.GetTrait(data.ID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if trait == nil {
		return protocol.ErrResponse(fmt.Sprintf("trait %q not found", data.ID))
	}
	if method == "traits.edit" {
		owned, err := e.store.SessionOwnsTrait(source.SessionID, trait.ID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if !owned {
			return protocol.ErrResponse("agents may edit only traits assigned to their own session")
		}
		// Only the prompt may change; names, defaults and assignments are
		// deliberately outside this capability. Do not parse bodies as flags.
		if err := e.store.UpdateTrait(trait.ID, nil, data.Body, nil); err != nil {
			return protocol.ErrResponse(err.Error())
		}
	}
	return e.ShowTrait([]string{"--name=" + trait.ID})
}
