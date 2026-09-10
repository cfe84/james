package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"james/moneypenny/pkg/envelope"
)

func TestTraitGadgetGatewayAuthorizationAndRouting(t *testing.T) {
	h, params := gadgetTestHandler(t)
	socket := fmt.Sprintf(".traits-%d.sock", time.Now().UnixNano())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var req gadgetHemRequest
			if err := json.NewDecoder(conn).Decode(&req); err == nil {
				calls.Add(1)
				var route struct {
					Source string          `json:"source_session_id"`
					Method string          `json:"method"`
					Data   json.RawMessage `json:"data"`
				}
				if len(req.Args) != 1 || json.Unmarshal([]byte(req.Args[0]), &route) != nil ||
					req.Verb != "gadget" || req.Noun != "route" || route.Source != gadgetSession ||
					(route.Method != "traits.list" && route.Method != "traits.get" && route.Method != "traits.edit") {
					t.Errorf("unbound or unrestricted route: %+v %+v", req, route)
				}
				data, err := envelope.DecodeTraitGadget(route.Method, route.Data)
				if err != nil || (route.Method == "traits.edit" && (data.Body == nil || *data.Body != " \n--name=forged\n")) {
					t.Errorf("body not preserved: %+v %v", data, err)
				}
				_ = json.NewEncoder(conn).Encode(gadgetHemResponse{Status: "ok", RequestID: req.RequestID, Data: json.RawMessage(`{"routed":true}`)})
			}
			_ = conn.Close()
		}
	}()
	defer func() { _ = listener.Close(); <-done }()
	raw, _ := json.Marshal(envelope.SessionIDData{SessionID: gadgetSession, GadgetRoute: map[string]string{"JAMES_HEM_SOCKET": socket}})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "get_session", Data: raw}); resp.Status != envelope.StatusSuccess {
		t.Fatal(resp)
	}
	cases := []struct {
		method string
		data   any
	}{
		{"traits.list", map[string]string{}},
		{"traits.get", map[string]string{"id": "existing"}},
		{"traits.edit", map[string]string{"id": "existing", "body": " \n--name=forged\n"}},
	}
	for _, tc := range cases {
		requireGadgetError(t, gadgetCall(t, params, tc.method, tc.data), "permission_denied")
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Agents: true})
	requireGadgetError(t, gadgetCall(t, params, "traits.list", map[string]string{}), "permission_denied")
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Traits: true})
	for _, tc := range cases {
		if resp := gadgetCall(t, params, tc.method, tc.data); !resp.Success {
			t.Fatalf("allowed %s failed: %+v", tc.method, resp)
		}
	}
	for _, method := range []string{"traits.create", "traits.delete", "traits.assign", "update_trait", "gadget.route", "subagents.create", "agents.list"} {
		code := "unknown_method"
		if method == "subagents.create" || method == "agents.list" {
			code = "permission_denied"
		}
		requireGadgetError(t, gadgetCall(t, params, method, map[string]string{"id": "existing"}), code)
	}
	for _, tc := range []struct{ method, data string }{
		{"traits.list", `null`},
		{"traits.list", `{"method":"traits.edit"}`},
		{"traits.get", `{}`},
		{"traits.get", `{"id":"existing","body":"bad"}`},
		{"traits.edit", `{"id":"existing"}`},
		{"traits.edit", `{"id":"existing","body":null}`},
		{"traits.edit", `{"id":"existing","body":7}`},
		{"traits.edit", `{"id":"existing","body":"bad","name":"new"}`},
		{"traits.edit", `{"id":"existing","body":"bad","default":true}`},
		{"traits.edit", `{"id":"existing","body":"bad","source_session_id":"other"}`},
		{"traits.edit", `{"id":"existing","body":"bad","gadget_route":{}}`},
		{"traits.edit", `{"id":"existing","body":"bad","gadget_capabilities":{"traits":true}}`},
	} {
		requireGadgetError(t, gadgetCall(t, params, tc.method, json.RawMessage(tc.data)), "invalid_request")
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	for _, tc := range cases {
		requireGadgetError(t, gadgetCall(t, params, tc.method, tc.data), "permission_denied")
	}
	if calls.Load() != 3 {
		t.Fatalf("denied or malformed requests reached Hem: %d", calls.Load())
	}
}

func TestTraitGadgetPromptReflectsGrantAndRevocation(t *testing.T) {
	h, params := gadgetTestHandler(t)
	if strings.Contains(params.SystemPrompt, "gadgets traits") {
		t.Fatal("traits advertised without opt-in")
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Traits: true})
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"gadgets traits list", "gadgets traits get ID", "gadgets traits edit ID", "future use by all agents", "complete replacement body on stdin"} {
		if !strings.Contains(params.SystemPrompt, text) {
			t.Fatalf("missing trait instruction: %s", text)
		}
	}
	for _, method := range []string{"create", "delete", "assign"} {
		if strings.Contains(params.SystemPrompt, "gadgets traits "+method) {
			t.Fatalf("unsupported method advertised: %s", method)
		}
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(params.SystemPrompt, "gadgets traits") {
		t.Fatal("revoked traits still advertised")
	}
}
