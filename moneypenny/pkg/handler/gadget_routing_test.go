package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/store"
)

func gadgetRoutingHandler(t *testing.T, environment map[string]string) *Handler {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	raw, _ := json.Marshal(environment)
	if err := s.CreateSession(&store.Session{SessionID: "bound-session", Environment: string(raw)}); err != nil {
		t.Fatal(err)
	}
	return &Handler{store: s, dataDir: t.TempDir()}
}

func TestRouteGadgetUsesBoundSessionAndStoredSocket(t *testing.T) {
	socket := fmt.Sprintf(".gadget-%d.sock", time.Now().UnixNano())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := gadgetRoutingHandler(t, map[string]string{"JAMES_HEM_SOCKET": socket})
	t.Setenv("JAMES_HEM_SOCKET", "/not/the/trusted/socket")
	requests := make(chan gadgetHemRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request gadgetHemRequest
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(gadgetHemResponse{Status: "ok", RequestID: request.RequestID, Data: json.RawMessage(`{"agents":[]}`)})
	}()
	got, err := h.routeGadget(context.Background(), "bound-session", "agents.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got.(json.RawMessage)) != `{"agents":[]}` {
		t.Fatalf("unexpected result: %s", got)
	}
	request := <-requests
	var payload struct {
		Source string `json:"source_session_id"`
		Method string `json:"method"`
	}
	if len(request.Args) != 1 || json.Unmarshal([]byte(request.Args[0]), &payload) != nil {
		t.Fatalf("bad request: %#v", request)
	}
	if request.Verb != "gadget" || request.Noun != "route" || payload.Source != "bound-session" || payload.Method != "agents.list" {
		t.Fatalf("unbound route: %#v, %#v", request, payload)
	}
}

func TestRouteGadgetRejectsMissingRouteAndUnsupportedMethods(t *testing.T) {
	h := gadgetRoutingHandler(t, nil)
	t.Setenv("JAMES_HEM_ADDRESS", "untrusted/control")
	for _, method := range []string{"agents.list", "agents.delete", "subagents.stop"} {
		if _, err := h.routeGadget(context.Background(), "bound-session", method, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("%s unexpectedly succeeded", method)
		}
	}
	if _, err := h.routeGadget(context.Background(), "missing", "agents.list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unbound session accepted")
	}
	h = gadgetRoutingHandler(t, map[string]string{"JAMES_HEM_ADDRESS": "relay/control", "JAMES_HEM_SOCKET": "fallback.sock"})
	if _, err := h.routeGadget(context.Background(), "bound-session", "agents.list", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "FINGERPRINT") {
		t.Fatalf("missing pin did not fail closed: %v", err)
	}
}

func TestRouteGadgetHonorsCancellation(t *testing.T) {
	socket := fmt.Sprintf(".gadget-%d.sock", time.Now().UnixNano())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	h := gadgetRoutingHandler(t, map[string]string{"JAMES_HEM_SOCKET": socket})
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request gadgetHemRequest
		_ = json.NewDecoder(conn).Decode(&request)
		var one [1]byte
		_, _ = conn.Read(one[:])
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := h.routeGadget(ctx, "bound-session", "agents.list", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unresponsive Hem did not fail")
	}
	if time.Since(start) > time.Second {
		t.Fatal("route ignored context deadline")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("route did not close its connection")
	}
}

func TestGadgetHemResponses(t *testing.T) {
	for name, response := range map[string]string{
		"error":     `{"status":"error","request_id":"mine","message":"denied"}`,
		"missing":   `{"status":"ok","request_id":"mine"}`,
		"unmatched": `{"status":"ok","request_id":"other","data":{}}`,
		"malformed": `{`,
		"oversized": strings.Repeat("x", 4*1024*1024+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readGadgetHemResponse(strings.NewReader(response), "mine"); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	result, err := readGadgetHemResponse(strings.NewReader(
		"{\"verb\":\"gadget\",\"request_id\":\"mine\"}\n"+
			"{\"status\":\"ok\",\"request_id\":\"other\",\"data\":{}}\n"+
			"{\"status\":\"ok\",\"request_id\":\"mine\",\"data\":{\"accepted\":true}}\n"), "mine")
	if err != nil || string(result.(json.RawMessage)) != `{"accepted":true}` {
		t.Fatalf("response correlation failed: %s, %v", result, err)
	}
}

func TestGadgetMI6UsesDaemonIdentityAndPinnedRoute(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\nread request\nprintf '%s\\n' '{\"status\":\"ok\",\"request_id\":\"mine\",\"data\":{\"remote\":true}}'\n"
	if err := os.WriteFile(filepath.Join(dir, "mi6-client"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	h := &Handler{dataDir: dir}
	result, err := h.routeGadgetMI6(context.Background(), "relay.example/control", "SHA256:trusted", gadgetHemRequest{RequestID: "mine"})
	if err != nil || string(result.(json.RawMessage)) != `{"remote":true}` {
		t.Fatalf("remote route failed: %s, %v", result, err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "--line-mode\n--key\n" + filepath.Join(dir, "moneypenny_ecdsa") + "\n--server-fingerprint\nSHA256:trusted\nrelay.example/control\n"
	if string(args) != want {
		t.Fatalf("wrong relay identity/route: %s", args)
	}
}
