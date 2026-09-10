package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/envelope"
)

func decodeGadgetData(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return fmt.Errorf("gadget data must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("gadget data must contain one JSON object")
	}
	return nil
}

// GadgetRoute is an internal daemon-to-Hem operation, not an agent CLI surface.
// Agents reach it only via their authenticated, capability-checked daemon.
func (e *Executor) GadgetRoute(args []string) *protocol.Response {
	if len(args) != 1 {
		return protocol.ErrResponse("gadget route requires one JSON payload")
	}
	var route protocol.GadgetRoute
	if err := decodeGadgetData([]byte(args[0]), &route); err != nil {
		return protocol.ErrResponse(err.Error())
	}
	source, err := e.store.GetSession(route.SourceSessionID)
	if err != nil || source == nil {
		return protocol.ErrResponse("gadget source session is not tracked by this Hem")
	}
	switch route.Method {
	case "traits.list", "traits.get", "traits.edit":
		return e.traitsGadget(source, route.Method, route.Data)
	case "agents.list", "subagents.list":
		if err := decodeGadgetData(route.Data, &struct{}{}); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		return e.listGadgetAgents(source, route.Method == "subagents.list")
	case "agents.message", "subagents.message":
		var message struct {
			ID   string `json:"id"`
			Body string `json:"body"`
		}
		if err := decodeGadgetData(route.Data, &message); err != nil {
			return protocol.ErrResponse(err.Error())
		}
		target, err := e.store.GetSession(message.ID)
		if err != nil || target == nil {
			return protocol.ErrResponse("gadget target session is not tracked by this Hem")
		}
		if route.Method == "subagents.message" && !gadgetRelated(source, target) {
			return protocol.ErrResponse("subagents may message only direct children or their parent")
		}
		return e.messageGadgetAgent(source, target, message.Body)
	case "subagents.create", "agents.create":
		create, err := envelope.DecodeCreateAgentGadget(route.Data)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		mp, err := e.store.GetMoneypenny(source.MoneypennyName)
		if err != nil || mp == nil {
			return protocol.ErrResponse("gadget source moneypenny is not registered")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		detail, err := e.sendCommand(ctx, mp, "get_session", map[string]interface{}{"session_id": source.SessionID})
		if err != nil {
			return protocol.ErrResponse(fmt.Sprintf("getting parent permissions: %v", err))
		}
		if detail.Status != envelope.StatusSuccess {
			return protocol.ErrResponse("invalid response while getting creation permissions")
		}
		capabilities, err := sessionGadgetCapabilities(detail.Data)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if route.Method == "subagents.create" && !capabilities.Subagents {
			return protocol.ErrResponse("parent subagents capability is disabled")
		}
		if route.Method == "agents.create" && !capabilities.CreateAgents {
			return protocol.ErrResponse("create agents capability is disabled")
		}
		createArgs := []string{
			"--from=" + source.SessionID, "--async", "--gadgets",
			fmt.Sprintf("--gadget-memory=%t", capabilities.Memory),
			fmt.Sprintf("--gadget-subagents=%t", capabilities.Subagents),
			fmt.Sprintf("--gadget-agents=%t", capabilities.Agents),
			fmt.Sprintf("--gadget-create-agents=%t", capabilities.CreateAgents),
			fmt.Sprintf("--gadget-traits=%t", capabilities.Traits),
			fmt.Sprintf("--gadget-scheduling=%t", capabilities.Scheduling),
		}
		if route.Method == "agents.create" {
			target := create.Moneypenny
			if target == "" {
				target = source.MoneypennyName
			}
			createArgs = append(createArgs, "--moneypenny="+target)
		} else {
			createArgs = append(createArgs, "--session-id="+source.SessionID)
			if create.Moneypenny != "" {
				createArgs = append(createArgs, "--moneypenny="+create.Moneypenny)
			}
		}
		for key, value := range map[string]string{"name": create.Name, "agent": create.Agent, "model": create.Model, "path": create.Path} {
			if value != "" {
				createArgs = append(createArgs, "--"+key+"="+value)
			}
		}
		if create.Traits != nil {
			createArgs = append(createArgs, "--traits="+*create.Traits)
		}
		// Delimit user-controlled text so it cannot become an operator flag.
		if route.Method == "agents.create" {
			return e.CreateSession(append(createArgs, "--", create.Prompt))
		}
		return e.CreateSubSession(append(createArgs, "--", create.Prompt))
	default:
		return protocol.ErrResponse("unsupported gadget routing method")
	}
}

func gadgetRelated(source, target *store.Session) bool {
	return source.SessionID != target.SessionID &&
		(target.ParentSessionID == source.SessionID || source.ParentSessionID == target.SessionID)
}

type gadgetAgent struct {
	SessionID       string `json:"session_id"`
	Name            string `json:"name,omitempty"`
	Nick            string `json:"nick,omitempty"`
	Agent           string `json:"agent,omitempty"`
	Status          string `json:"status"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
}

func (e *Executor) listGadgetAgents(source *store.Session, scoped bool) *protocol.Response {
	sessions, err := e.store.ListTrackedSessions("")
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	cached := e.getMPData()
	result := make([]gadgetAgent, 0)
	for _, session := range sessions {
		if scoped && !gadgetRelated(source, session) {
			continue
		}
		info := cached[session.MoneypennyName][session.SessionID]
		status := info.Status
		if status == "" {
			status = session.HemStatus
		}
		result = append(result, gadgetAgent{session.SessionID, info.Name, session.Nick, info.Agent, status, session.ParentSessionID})
	}
	return protocol.OKResponse(map[string]any{"agents": result})
}

func (e *Executor) messageGadgetAgent(source, target *store.Session, body string) *protocol.Response {
	if strings.TrimSpace(body) == "" {
		return protocol.ErrResponse("agent message body is required")
	}
	mp, err := e.store.GetMoneypenny(target.MoneypennyName)
	if err != nil || mp == nil {
		return protocol.ErrResponse("target moneypenny is not registered")
	}
	data := map[string]interface{}{
		"session_id": target.SessionID, "prompt": body,
		"source_session_id": source.SessionID, "source_name": e.agentOriginLabel(source.SessionID),
	}
	if source.ParentSessionID == target.SessionID {
		data["source"] = "callback"
	}
	if target.HemStatus == "completed" {
		_ = e.store.SetSessionHemStatus(target.SessionID, "active")
	}
	_ = e.store.SetSessionReviewed(target.SessionID, false)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = e.sendCommand(ctx, mp, "continue_session", data)
	queued := false
	if isSessionNotIdle(err) {
		_, err = e.sendCommand(ctx, mp, "queue_prompt", data)
		queued = true
	}
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	e.invalidateMPCache(mp.Name)
	return protocol.OKResponse(SessionContinuedResult{SessionID: target.SessionID, Async: !queued, Queued: queued})
}
