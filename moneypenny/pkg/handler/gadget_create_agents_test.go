package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
)

func TestCreateAgentsGadgetGrantBindingAndRevocation(t *testing.T) {
	h, params := gadgetTestHandler(t)
	prompt := map[string]string{"prompt": "--from=forged"}
	requireGadgetError(t, gadgetCall(t, params, "agents.create", prompt), "permission_denied")
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Agents: true, Subagents: true})
	requireGadgetError(t, gadgetCall(t, params, "agents.create", prompt), "permission_denied")

	socket := fmt.Sprintf(".create-agents-%d.sock", time.Now().UnixNano())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan gadgetHemRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request gadgetHemRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(gadgetHemResponse{Status: "ok", RequestID: request.RequestID, Data: json.RawMessage(`{"session_id":"created","async":true}`)})
	}()
	raw, _ := json.Marshal(envelope.SessionIDData{SessionID: gadgetSession, GadgetRoute: map[string]string{"JAMES_HEM_SOCKET": socket}})
	if response := h.Handle(context.Background(), &envelope.Command{Method: "get_session", Data: raw}); response.Status != envelope.StatusSuccess {
		t.Fatal(response)
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{CreateAgents: true})
	for _, data := range []string{
		`null`, `{}`, `{"prompt":null}`, `{"prompt":" "}`,
		`{"prompt":"p","source_session_id":"other"}`, `{"prompt":"p","from":"other"}`,
		`{"prompt":"p","gadget_capabilities":{"agents":true}}`, `{"prompt":"p","parent":"other"}`,
	} {
		requireGadgetError(t, gadgetCall(t, params, "agents.create", json.RawMessage(data)), "invalid_request")
	}
	for _, method := range []string{"agents.list", "agents.message", "subagents.create"} {
		requireGadgetError(t, gadgetCall(t, params, method, prompt), "permission_denied")
	}
	if response := gadgetCall(t, params, "agents.create", prompt); !response.Success {
		t.Fatal(response)
	}
	select {
	case request := <-requests:
		var route struct {
			Source string                         `json:"source_session_id"`
			Method string                         `json:"method"`
			Data   envelope.CreateAgentGadgetData `json:"data"`
		}
		if len(request.Args) != 1 || json.Unmarshal([]byte(request.Args[0]), &route) != nil ||
			route.Source != gadgetSession || route.Method != "agents.create" || route.Data.Prompt != prompt["prompt"] {
			t.Fatalf("creation not bound to credential: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("creation not forwarded")
	}
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params.SystemPrompt, "gadgets agents create") || strings.Contains(params.SystemPrompt, "gadgets agents list") {
		t.Fatal("prompt does not reflect independent creation grant")
	}
	yolo := true
	update, _ := json.Marshal(envelope.UpdateSessionData{SessionID: gadgetSession, Yolo: &yolo})
	if response := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: update}); response.Status != envelope.StatusSuccess {
		t.Fatal(response)
	}
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params.SystemPrompt, "gadgets agents create") || !strings.Contains(params.SystemPrompt, "[--yolo]") {
		t.Fatal("yolo source was not authorized to request yolo creation")
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	requireGadgetError(t, gadgetCall(t, params, "agents.create", prompt), "permission_denied")
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(params.SystemPrompt, "gadgets agents create") {
		t.Fatal("revoked creation still advertised")
	}
}
