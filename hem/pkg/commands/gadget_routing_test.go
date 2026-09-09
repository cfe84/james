package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/envelope"
)

func gadgetRouteRequest(t *testing.T, e *Executor, source, method string, data any) *protocol.Response {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(protocol.GadgetRoute{SourceSessionID: source, Method: method, Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	return e.Dispatch("gadget", "route", []string{string(payload)})
}

func TestGadgetAuthoritativeRelationshipScope(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp", TransportType: store.TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "source", "child", "sibling", "grandchild", "unrelated"} {
		if err := e.store.TrackSession(id, "mp"); err != nil {
			t.Fatal(err)
		}
	}
	for id, parent := range map[string]string{"source": "parent", "child": "source", "sibling": "parent", "grandchild": "child"} {
		if err := e.store.SetSessionParent(id, parent); err != nil {
			t.Fatal(err)
		}
	}
	list := func(method string) map[string]bool {
		t.Helper()
		response := gadgetRouteRequest(t, e, "source", method, map[string]any{})
		if response.Status != "ok" {
			t.Fatal(response.Message)
		}
		var result struct {
			Agents []gadgetAgent `json:"agents"`
		}
		if err := json.Unmarshal(response.Data, &result); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, agent := range result.Agents {
			ids[agent.SessionID] = true
		}
		return ids
	}
	if got := list("subagents.list"); !reflect.DeepEqual(got, map[string]bool{"parent": true, "child": true}) {
		t.Fatalf("wrong scoped list: %v", got)
	}
	if got := list("agents.list"); len(got) != 6 {
		t.Fatalf("combined agents list should include all tracked agents: %v", got)
	}
	for _, id := range []string{"source", "sibling", "grandchild", "unrelated"} {
		response := gadgetRouteRequest(t, e, "source", "subagents.message", map[string]any{"id": id, "body": "hello"})
		if response.Status != "error" || !strings.Contains(response.Message, "direct children") {
			t.Fatalf("unrelated target %q accepted: %#v", id, response)
		}
	}
	if err := e.store.SetSessionParent("child", "unrelated"); err != nil {
		t.Fatal(err)
	}
	if got := list("subagents.list"); !reflect.DeepEqual(got, map[string]bool{"parent": true}) {
		t.Fatalf("stale scope after reparenting: %v", got)
	}
	for _, tc := range []struct {
		source, method string
		data           any
	}{
		{"missing", "agents.list", map[string]any{}},
		{"source", "agents.delete", map[string]any{"id": "child"}},
		{"source", "subagents.create", map[string]any{"prompt": "hello", "parent": "unrelated"}},
		{"source", "subagents.create", map[string]any{"prompt": "hello", "gadget_capabilities": map[string]bool{"agents": true}}},
		{"source", "agents.message", map[string]any{"id": "child", "body": "hello", "source_session_id": "parent"}},
		{"source", "agents.list", nil},
	} {
		if response := gadgetRouteRequest(t, e, tc.source, tc.method, tc.data); response.Status != "error" {
			t.Fatalf("invalid route accepted: %#v", tc)
		}
	}
}

func TestGadgetRoutingCreateAndMessage(t *testing.T) {
	e := newHierarchyExecutor(t)
	dir := t.TempDir()
	inPath, outPath := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	for _, path := range []string{inPath, outPath} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	in, err := os.OpenFile(inPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	defer func() { _, _ = in.WriteString("\n") }()
	writes := make(chan transport.Command, 10)
	capabilities := envelope.GadgetCapabilities{Subagents: true, Memory: false, Agents: false, Scheduling: false}
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var command transport.Command
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				return
			}
			data := any(map[string]any{})
			status := "ok"
			errorCode := ""
			switch command.Method {
			case "get_session":
				data = map[string]any{
					"agent": "copilot", "path": "/parent", "yolo": false, "gadget_capabilities": capabilities,
					"environment": map[string]string{"KEEP": "value", "JAMES_HEM_ADDRESS": "stale/route"},
				}
			case "summarize_session":
				data = map[string]any{"summary": "Prior session context", "turn_count": 1}
			case "create_session", "queue_prompt", "update_session":
				writes <- command
			case "continue_session":
				writes <- command
				status, errorCode = "error", "SESSION_NOT_IDLE"
			}
			raw, _ := json.Marshal(data)
			if json.NewEncoder(out).Encode(transport.Response{Status: status, ErrorCode: errorCode, Data: raw}) != nil {
				return
			}
		}
	}()
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"source", "target", "parent"} {
		if err := e.store.TrackSession(id, "mp"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.SetSessionParent("source", "parent"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSessionNick("source", "Trusted source"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetCachedModels("mp", "copilot", []store.CachedModelEntry{{Name: "model", Value: "model"}}); err != nil {
		t.Fatal(err)
	}
	next := func() transport.Command {
		t.Helper()
		select {
		case command := <-writes:
			return command
		case <-time.After(time.Second):
			t.Fatal("no daemon command received")
			return transport.Command{}
		}
	}
	const prompt = "--gadget-agents=true"
	response := gadgetRouteRequest(t, e, "source", "subagents.create", map[string]any{"prompt": prompt, "agent": "copilot", "name": "--yolo"})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	command := next()
	data := command.Data.(map[string]any)
	if data["prompt"] != prompt || data["name"] != "--yolo" || data["source_session_id"] != "source" || data["yolo"] == true {
		t.Fatalf("creation data was not safely bound: %#v", data)
	}
	raw, _ := json.Marshal(data["gadget_capabilities"])
	var inherited envelope.GadgetCapabilities
	if err := json.Unmarshal(raw, &inherited); err != nil || inherited != capabilities {
		t.Fatalf("permissions broadened: %s", raw)
	}
	child, err := e.store.GetSession(data["session_id"].(string))
	if err != nil || child == nil || child.ParentSessionID != "source" {
		t.Fatalf("child not bound to authenticated source: %#v %v", child, err)
	}
	if data["environment"].(map[string]any)["JAMES_HEM_SOCKET"] == "" {
		t.Fatal("child route not persisted")
	}
	for _, target := range []string{"target", "parent"} {
		const body = " \n--from=forged\nverbatim body\n "
		method := "agents.message"
		if target == "parent" {
			method = "subagents.message"
		}
		response = gadgetRouteRequest(t, e, "source", method, map[string]any{"id": target, "body": body})
		if response.Status != "ok" {
			t.Fatal(response.Message)
		}
		for _, wantMethod := range []string{"continue_session", "queue_prompt"} {
			command = next()
			data = command.Data.(map[string]any)
			if command.Method != wantMethod || data["prompt"] != body || data["source_session_id"] != "source" || data["source_name"] != "Trusted source" {
				t.Fatalf("message lost provenance/body: %#v", command)
			}
			if target == "parent" && data["source"] != "callback" {
				t.Fatal("parent reply did not use callback role")
			}
		}
	}
	for name, call := range map[string]func() *protocol.Response{
		"create": func() *protocol.Response {
			return e.CreateSession([]string{"-m=mp", "--agent=copilot", "--async", "--env=JAMES_HEM_ADDRESS=untrusted/route", "hello"})
		},
		"copy": func() *protocol.Response {
			return e.CopySession([]string{"source", "--async"})
		},
		"update": func() *protocol.Response {
			return e.UpdateSession([]string{"source", "--name=renamed"})
		},
	} {
		t.Run(name+" persists trusted route", func(t *testing.T) {
			response := call()
			if response.Status != "ok" {
				t.Fatal(response.Message)
			}
			command := next()
			data := command.Data.(map[string]any)
			environment := data["environment"].(map[string]any)
			if environment["JAMES_HEM_SOCKET"] == nil || environment["JAMES_HEM_SOCKET"] == "untrusted.sock" || environment["JAMES_HEM_ADDRESS"] != nil {
				t.Fatalf("untrusted or missing route: %#v", environment)
			}
			if name == "update" && environment["KEEP"] != "value" {
				t.Fatalf("update lost unrelated environment: %#v", environment)
			}
		})
	}
}
