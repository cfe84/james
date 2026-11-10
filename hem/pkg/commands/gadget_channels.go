package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

func (e *Executor) channelsGadget(source *store.Session, method string, data json.RawMessage) *protocol.Response {
	caps, err := e.currentGadgetCapabilities(source)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if !caps.Channels {
		return protocol.ErrResponse("channel configuration capability is disabled")
	}
	var request struct {
		ChannelID   int64   `json:"channel_id"`
		Provider    *string `json:"provider"`
		Query       *string `json:"query"`
		TargetID    *string `json:"target_id"`
		Label       *string `json:"label"`
		Mention     *string `json:"mention"`
		AllowAnyone *bool   `json:"allow_anyone"`
	}
	if err := decodeGadgetData(data, &request); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	action := strings.TrimPrefix(method, "channels.")
	args := []string{"--session-id=" + source.SessionID}
	for _, field := range []struct {
		name    string
		value   *string
		actions string
	}{
		{"provider", request.Provider, "search create"},
		{"query", request.Query, "search"},
		{"target-id", request.TargetID, "create"},
		{"label", request.Label, "create"},
		{"mention", request.Mention, "create set"},
	} {
		if field.value == nil {
			continue
		}
		if !strings.Contains(" "+field.actions+" ", " "+action+" ") {
			return protocol.ErrResponse("unexpected channel field: " + field.name)
		}
		args = append(args, "--"+field.name+"="+*field.value)
	}
	if request.AllowAnyone != nil {
		if action != "create" && action != "set" {
			return protocol.ErrResponse("unexpected channel field: allow_anyone")
		}
		if *request.AllowAnyone {
			args = append(args, "--allow-anyone")
		} else if action == "set" {
			args = append(args, "--owner-only")
		}
	}
	mutation := action == "set" || action == "enable" || action == "disable" || action == "delete"
	if mutation {
		if request.ChannelID < 1 {
			return protocol.ErrResponse("channel_id must be positive")
		}
		mp, err := e.resolveSessionMoneypenny(source.SessionID)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		response, err := e.sendCommand(context.Background(), mp, "list_channels", map[string]any{"session_id": source.SessionID})
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		var result struct {
			Channels []channelInfoWire `json:"channels"`
		}
		if err := json.Unmarshal(response.Data, &result); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		owned := false
		for _, ch := range result.Channels {
			if ch.ID == request.ChannelID && ch.SessionID == source.SessionID {
				owned = true
				break
			}
		}
		if !owned {
			return protocol.ErrResponse("channel does not belong to this session")
		}
		args = append(args, fmt.Sprint(request.ChannelID))
	} else if request.ChannelID != 0 {
		return protocol.ErrResponse("unexpected channel_id")
	}
	switch action {
	case "providers":
		return e.ListChannelProviders(args)
	case "search":
		return e.SearchChannelTargets(args)
	case "list":
		return e.ListChannels(args)
	case "create":
		return e.CreateChannel(args)
	case "set":
		return e.UpdateChannel(args)
	case "enable", "disable":
		return e.SetChannelEnabled(args, action == "enable")
	case "delete":
		return e.DeleteChannel(args)
	}
	return protocol.ErrResponse("unsupported channel gadget")
}
