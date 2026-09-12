package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
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
	capabilities := envelope.GadgetCapabilities{Subagents: true, Memory: false, Agents: false, Traits: true, Scheduling: false, CreateAgents: true, MoneypennyLogs: true}
	var liveCapabilities atomic.Value
	liveCapabilities.Store(capabilities)
	var sourceYolo atomic.Bool
	go func() {
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var command transport.Command
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				return
			}
			data := any(map[string]any{})
			status := envelope.StatusSuccess
			errorCode := ""
			switch command.Method {
			case "get_session":
				data = map[string]any{
					"agent": "copilot", "path": "/parent", "yolo": sourceYolo.Load(), "gadget_capabilities": liveCapabilities.Load(),
					"environment": map[string]string{"KEEP": "value", "JAMES_HEM_ADDRESS": "stale/route"},
				}
			case "summarize_session":
				data = map[string]any{"summary": "Prior session context", "turn_count": 1}
			case "get_logs":
				writes <- command
				switch command.Data.(map[string]any)["lines"] {
				case float64(100):
					data = envelope.GetLogsResponse{Content: "recent log", Lines: 1}
				case float64(2000):
					data = envelope.GetLogsResponse{Content: "bounded tail", Lines: 1, Truncated: true}
				default:
					status, errorCode = "error", envelope.ErrInvalidRequest
					data = map[string]string{"message": "lines must be between 1 and 10000"}
				}
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
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "remote", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
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
	for _, tc := range []struct {
		name  string
		data  map[string]any
		lines int
		want  string
	}{
		{"own host", map[string]any{}, 100, "recent log"},
		{"remote host", map[string]any{"name": "remote", "lines": 2000}, 2000, "[earlier log output omitted because the final 2 MiB limit was reached]\nbounded tail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := gadgetRouteRequest(t, e, "source", "moneypenny.logs", tc.data)
			var result TextResult
			if response.Status != "ok" || json.Unmarshal(response.Data, &result) != nil || result.Message != tc.want {
				t.Fatalf("unexpected log response: %+v", response)
			}
			if cmd := next(); cmd.Method != "get_logs" || cmd.Data.(map[string]any)["lines"] != float64(tc.lines) {
				t.Fatalf("wrong log request: %+v", cmd)
			}
		})
	}
	for _, data := range []string{
		`null`, `{"path":"secret"}`, `{"source_session_id":"other"}`, `{"gadget_route":{}}`,
		`{"lines":"100"}`, `{"lines":0}`, `{"lines":-1}`, `{"name":"missing"}`, `{"name":"--lines=1"}`,
	} {
		if response := gadgetRouteRequest(t, e, "source", "moneypenny.logs", json.RawMessage(data)); response.Status != "error" {
			t.Fatalf("invalid logs request accepted: %s", data)
		}
	}
	deniedLogs := capabilities
	deniedLogs.MoneypennyLogs = false
	liveCapabilities.Store(deniedLogs)
	if response := gadgetRouteRequest(t, e, "source", "moneypenny.logs", map[string]any{}); response.Status != "error" || !strings.Contains(response.Message, "capability is disabled") {
		t.Fatalf("logs revocation ignored: %+v", response)
	}
	select {
	case cmd := <-writes:
		t.Fatalf("denied or malformed request reached logs: %+v", cmd)
	default:
	}
	liveCapabilities.Store(capabilities)
	if response := gadgetRouteRequest(t, e, "source", "moneypenny.logs", map[string]any{"lines": 10001}); response.Status != "error" || !strings.Contains(response.Message, envelope.ErrInvalidRequest) {
		t.Fatalf("daemon error not propagated: %+v", response)
	}
	next()
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
	if data["gadget_route"].(map[string]any)["JAMES_HEM_SOCKET"] == "" {
		t.Fatal("child route not persisted")
	}
	// Top-level creation needs neither Subagents nor the discovery/message grant.
	topLevelCaps := capabilities
	topLevelCaps.Subagents = false
	liveCapabilities.Store(topLevelCaps)
	response = gadgetRouteRequest(t, e, "source", "agents.create", map[string]any{
		"prompt": "--from=forged", "agent": "copilot", "name": "--yolo", "path": "/independent",
	})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	command = next()
	data = command.Data.(map[string]any)
	if data["prompt"] != "--from=forged" || data["source_session_id"] != "source" ||
		data["source_name"] != "Trusted source" || data["name"] != "--yolo" ||
		data["path"] != "/independent" || data["yolo"] == true {
		t.Fatalf("top-level creation lost attribution or interpreted flags: %#v", data)
	}
	raw, _ = json.Marshal(data["gadget_capabilities"])
	if err := json.Unmarshal(raw, &inherited); err != nil || inherited != topLevelCaps {
		t.Fatalf("top-level permissions not inherited: %s", raw)
	}
	topLevel, err := e.store.GetSession(data["session_id"].(string))
	if err != nil || topLevel == nil || topLevel.ParentSessionID != "" || topLevel.MoneypennyName != "mp" {
		t.Fatalf("created agent is not independent on source host: %#v %v", topLevel, err)
	}
	response = gadgetRouteRequest(t, e, "source", "agents.create", map[string]any{"prompt": "denied yolo", "yolo": true})
	if response.Status != "error" || !strings.Contains(response.Message, "already has it") {
		t.Fatalf("non-yolo source requested License to Kill: %+v", response)
	}
	sourceYolo.Store(true)
	response = gadgetRouteRequest(t, e, "source", "agents.create", map[string]any{"prompt": "approved yolo", "yolo": true})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	if data := next().Data.(map[string]any); data["yolo"] != true || data["source_session_id"] != "source" {
		t.Fatalf("authorized License to Kill was not forwarded: %#v", data)
	}
	sourceYolo.Store(false)
	response = gadgetRouteRequest(t, e, "source", "agents.create", map[string]any{"prompt": "remote top level", "moneypenny": "remote"})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	data = next().Data.(map[string]any)
	remoteTopLevel, err := e.store.GetSession(data["session_id"].(string))
	if err != nil || remoteTopLevel == nil || remoteTopLevel.MoneypennyName != "remote" || remoteTopLevel.ParentSessionID != "" ||
		data["source_session_id"] != "source" {
		t.Fatalf("remote top-level creation was not bound correctly: %#v %#v %v", data, remoteTopLevel, err)
	}
	liveCapabilities.Store(capabilities)
	response = gadgetRouteRequest(t, e, "source", "subagents.create", map[string]any{"prompt": "remote child", "moneypenny": "remote"})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	data = next().Data.(map[string]any)
	remoteChild, err := e.store.GetSession(data["session_id"].(string))
	if err != nil || remoteChild == nil || remoteChild.MoneypennyName != "remote" || remoteChild.ParentSessionID != "source" ||
		data["source_session_id"] != "source" {
		t.Fatalf("remote subagent creation was not bound correctly: %#v %#v %v", data, remoteChild, err)
	}
	var created SessionCreatedResult
	if err := json.Unmarshal(response.Data, &created); err != nil || created.SessionID != remoteChild.SessionID || !created.Async {
		t.Fatalf("incorrect creation result: %s", response.Data)
	}
	for _, invalid := range []any{
		nil, map[string]any{}, map[string]any{"prompt": " "},
		map[string]any{"prompt": "hello", "from": "forged"},
		map[string]any{"prompt": "hello", "source_session_id": "forged"},
		map[string]any{"prompt": "hello", "parent": "source"},
		map[string]any{"prompt": "hello", "gadget_capabilities": map[string]bool{"agents": true}},
	} {
		if response := gadgetRouteRequest(t, e, "source", "agents.create", invalid); response.Status != "error" {
			t.Fatalf("invalid creation accepted: %#v", invalid)
		}
	}
	for _, denied := range []envelope.GadgetCapabilities{
		envelope.DefaultGadgetCapabilities(), {Agents: true, Subagents: true}, {},
	} {
		liveCapabilities.Store(denied)
		if response := gadgetRouteRequest(t, e, "source", "agents.create", map[string]string{"prompt": "denied"}); response.Status != "error" ||
			!strings.Contains(response.Message, "create agents capability is disabled") {
			t.Fatalf("creation grant not checked freshly: %+v", response)
		}
	}
	select {
	case command := <-writes:
		t.Fatalf("denied request created a session: %+v", command)
	default:
	}
	liveCapabilities.Store(capabilities)
	for _, trait := range []*store.Trait{
		{ID: "default-trait", Name: "Default trait", Prompt: "default trait instructions", EnabledByDefault: true},
		{ID: "selected-trait", Name: "--yolo", Prompt: "selected trait instructions"},
	} {
		if err := e.store.CreateTrait(trait); err != nil {
			t.Fatal(err)
		}
	}
	creationOnly := envelope.GadgetCapabilities{Subagents: true, CreateAgents: true}
	liveCapabilities.Store(creationOnly)
	stringPtr := func(value string) *string { return &value }
	for _, method := range []string{"agents.create", "subagents.create"} {
		for _, selection := range []struct {
			name string
			spec *string
			want []string
		}{
			{name: "omitted"},
			{name: "empty", spec: stringPtr("")},
			{name: "explicit", spec: stringPtr("--yolo,default-trait,--yolo"), want: []string{"selected-trait", "default-trait"}},
		} {
			t.Run(method+" traits "+selection.name, func(t *testing.T) {
				request := map[string]any{"prompt": "hello", "agent": "copilot"}
				if selection.spec != nil {
					request["traits"] = *selection.spec
				}
				want := selection.want
				if method == "agents.create" && selection.spec == nil {
					want = []string{"default-trait"}
				}
				response := gadgetRouteRequest(t, e, "source", method, request)
				if response.Status != "ok" {
					t.Fatal(response.Message)
				}
				data := next().Data.(map[string]any)
				id := data["session_id"].(string)
				got, err := e.store.GetSessionTraits(id)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("persisted traits = %v, want %v: %v", got, want, err)
				}
				systemPrompt, _ := data["system_prompt"].(string)
				for _, traitID := range []string{"default-trait", "selected-trait"} {
					trait, err := e.store.GetTrait(traitID)
					if err != nil {
						t.Fatal(err)
					}
					selected := false
					for _, id := range want {
						selected = selected || id == traitID
					}
					if strings.Contains(systemPrompt, trait.Prompt) != selected {
						t.Fatalf("trait composition mismatch: %q", systemPrompt)
					}
					if selected && strings.Index(systemPrompt, trait.Prompt) > strings.Index(systemPrompt, gadgetsMarker) {
						t.Fatal("traits must precede gadget instructions")
					}
				}
				if data["source_session_id"] != "source" || data["yolo"] == true {
					t.Fatalf("traits changed attribution or flags: %#v", data)
				}
			})
		}
		for _, spec := range []any{"missing-trait", 42, []string{"selected-trait"}} {
			response := gadgetRouteRequest(t, e, "source", method, map[string]any{"prompt": "hello", "traits": spec})
			if response.Status != "error" {
				t.Fatalf("invalid traits accepted: %v", spec)
			}
		}
	}
	select {
	case command := <-writes:
		t.Fatalf("invalid traits created session: %+v", command)
	default:
	}
	liveCapabilities.Store(capabilities)
	// Operator --from follows the same attribution path without making a child.
	response = e.CreateSession([]string{"-m=mp", "--agent=copilot", "--async", "--from=source", "--", "operator creation"})
	if response.Status != "ok" {
		t.Fatal(response.Message)
	}
	data = next().Data.(map[string]any)
	if data["source_session_id"] != "source" || data["source_name"] != "Trusted source" {
		t.Fatalf("operator create --from lost attribution: %#v", data)
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
			return e.CopySession([]string{"source", "--async", "--env", "KEEP=copy-value"})
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
			route := data["gadget_route"].(map[string]any)
			socket, _ := route["JAMES_HEM_SOCKET"].(string)
			if socket == "" || socket == "untrusted.sock" || route["JAMES_HEM_ADDRESS"] != nil {
				t.Fatalf("untrusted or missing internal route: %#v", route)
			}
			environment, _ := data["environment"].(map[string]any)
			if environment["JAMES_HEM_ADDRESS"] != nil || environment["JAMES_HEM_SOCKET"] != nil {
				t.Fatalf("route leaked into agent environment: %#v", environment)
			}
			if name == "update" && environment["KEEP"] != "value" {
				t.Fatalf("update lost unrelated environment: %#v", environment)
			}
			if name == "copy" && environment["KEEP"] != "copy-value" {
				t.Fatalf("copy did not accept the replacement environment: %#v", environment)
			}
		})
	}
}
