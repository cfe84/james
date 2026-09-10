package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/memory"
	"james/moneypenny/pkg/store"
)

const gadgetSession = "00000000-0000-0000-0000-000000000071"
const gadgetOtherSession = "00000000-0000-0000-0000-000000000072"

func gadgetTestHandler(t *testing.T) (*Handler, agent.RunParams) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "operational.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, agent.New(log.New(io.Discard, "", 0)), "test", dir)
	t.Cleanup(func() { _ = h.CloseGadgets(); _ = st.Close() })
	for _, id := range []string{gadgetSession, gadgetOtherSession} {
		if err := st.CreateSession(&store.Session{
			SessionID: id, Name: id, Agent: "copilot", Path: dir,
			Environment: `{"KEEP":"configured","JAMES_HEM_SOCKET":"operator-route","JAMES_GADGETS_TOKEN":"forged","JAMES_GADGETS_URL":"http://attacker"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.MigrateMemoryToSQLite(); err != nil {
		t.Fatal(err)
	}
	var params agent.RunParams
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	return h, params
}

func gadgetCall(t *testing.T, params agent.RunParams, method string, data any) gadgetResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"method": method, "data": data})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, params.Environment["JAMES_GADGETS_URL"], bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+params.Environment["JAMES_GADGETS_TOKEN"])
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result gadgetResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func setGadgetCaps(t *testing.T, h *Handler, caps envelope.GadgetCapabilities) {
	t.Helper()
	payload, _ := json.Marshal(envelope.UpdateSessionData{SessionID: gadgetSession, GadgetCapabilities: &caps})
	response := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: payload})
	if response.Status != envelope.StatusSuccess {
		t.Fatalf("updating capabilities: %+v", response)
	}
}

func requireGadgetError(t *testing.T, response gadgetResponse, code string) {
	t.Helper()
	if response.Success || response.Error == nil || response.Error.Code != code {
		t.Fatalf("expected %s, got %+v", code, response)
	}
}

func TestGadgetCredentialsAndLiveRevocation(t *testing.T) {
	h, params := gadgetTestHandler(t)
	token := params.Environment["JAMES_GADGETS_TOKEN"]
	if len(token) != 64 || token == "forged" || strings.Contains(params.SystemPrompt, token) {
		t.Fatal("credential was not securely bound outside prompt/config")
	}
	if params.Environment["JAMES_HEM_SOCKET"] != "" || params.Environment["KEEP"] != "configured" {
		t.Fatal("runtime leaked operator route or lost custom environment")
	}
	persisted, _ := h.store.GetSession(gadgetSession)
	if strings.Contains(persisted.Environment, token) {
		t.Fatal("runtime credential was persisted")
	}
	if response := gadgetCall(t, params, "memory.set", map[string]string{"body": "my knowledge"}); !response.Success {
		t.Fatalf("default memory denied: %+v", response)
	}
	requireGadgetError(t, gadgetCall(t, params, "agents.list", map[string]any{}), "permission_denied")
	requireGadgetError(t, gadgetCall(t, params, "memory.get", map[string]string{"session_id": gadgetOtherSession}), "invalid_request")

	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	for _, method := range []string{"memory.get", "subagents.list", "schedule.list", "agents.message"} {
		requireGadgetError(t, gadgetCall(t, params, method, map[string]any{}), "permission_denied")
	}
	if response := gadgetCall(t, params, "notify", map[string]string{"text": "Please approve access."}); !response.Success {
		t.Fatalf("revoked capabilities blocked notification: %+v", response)
	}
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Agents: true})
	requireGadgetError(t, gadgetCall(t, params, "delete_session", map[string]string{"session_id": gadgetOtherSession}), "unknown_method")
	requireGadgetError(t, gadgetCall(t, params, "subagents.create", map[string]string{"prompt": "no"}), "permission_denied")
	requireGadgetError(t, gadgetCall(t, params, "schedule.list", map[string]any{}), "permission_denied")
	setGadgetCaps(t, h, envelope.DefaultGadgetCapabilities())
	if response := gadgetCall(t, params, "memory.get", map[string]any{}); !response.Success {
		t.Fatalf("regrant not reflected by existing credential: %+v", response)
	}
	h.revokeGadgetToken(gadgetSession)
	requireGadgetError(t, gadgetCall(t, params, "notify", map[string]string{"text": "stale"}), "unauthorized")
}

func TestGadgetHTTPValidationAndSessionBinding(t *testing.T) {
	h, params := gadgetTestHandler(t)
	other := agent.RunParams{}
	if err := h.prepareRunInstructions(gadgetOtherSession, &other); err != nil {
		t.Fatal(err)
	}
	if other.Environment["JAMES_GADGETS_TOKEN"] == params.Environment["JAMES_GADGETS_TOKEN"] {
		t.Fatal("sessions share credential")
	}
	gadgetCall(t, params, "memory.set", map[string]string{"path": "private", "body": "only first"})
	requireGadgetError(t, gadgetCall(t, other, "memory.get", map[string]string{"path": "private"}), "not_found")
	for _, tc := range []struct{ name, body, auth, origin, method string }{
		{"missing auth", `{}`, "", "", "POST"},
		{"forged auth", `{}`, "Bearer forged", "", "POST"},
		{"browser", `{}`, "Bearer " + params.Environment["JAMES_GADGETS_TOKEN"], "https://example.com", "POST"},
		{"bad verb", `{}`, "Bearer " + params.Environment["JAMES_GADGETS_TOKEN"], "", "GET"},
		{"malformed", `{`, "Bearer " + params.Environment["JAMES_GADGETS_TOKEN"], "", "POST"},
		{"multiple", `{"method":"notify","data":{"text":"one"}} {}`, "Bearer " + params.Environment["JAMES_GADGETS_TOKEN"], "", "POST"},
		{"oversized", `{"method":"notify","data":{"text":"` + strings.Repeat("x", 1<<20) + `"}}`, "Bearer " + params.Environment["JAMES_GADGETS_TOKEN"], "", "POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, params.Environment["JAMES_GADGETS_URL"], strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.auth)
			req.Header.Set("Origin", tc.origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result gadgetResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Success || result.Error == nil || resp.StatusCode < 400 {
				t.Fatalf("request not rejected: %+v, %v", result, err)
			}
		})
	}
	if err := h.store.DeleteSession(gadgetSession); err != nil {
		t.Fatal(err)
	}
	requireGadgetError(t, gadgetCall(t, params, "notify", map[string]string{"text": "deleted"}), "unauthorized")
}

func TestGadgetMemoryAtomicAndOperatorSurfaces(t *testing.T) {
	h, params := gadgetTestHandler(t)
	root := h.memoryDir(gadgetSession)
	for _, count := range []int{4000, 4001} {
		resp := gadgetCall(t, params, "memory.set", map[string]string{"path": "unicode", "body": strings.Repeat("🕴", count)})
		if resp.Success != (count == 4000) {
			t.Fatalf("wrong Unicode limit at %d: %+v", count, resp)
		}
	}
	batch := map[string]any{"entries": []map[string]string{
		{"path": "", "body": "changed root"},
		{"path": "split/child", "body": strings.Repeat("x", 4001)},
	}}
	if resp := gadgetCall(t, params, "memory.batch", batch); resp.Success {
		t.Fatal("invalid batch succeeded")
	}
	node, _ := memory.Get(root, "")
	if node.Body != rootReadmeTemplate {
		t.Fatal("failed batch changed root")
	}
	batch["entries"] = []map[string]string{
		{"path": "", "body": "# Root\nRead split/child for knowledge."},
		{"path": "split/child", "body": "durable"},
	}
	if resp := gadgetCall(t, params, "memory.batch", batch); !resp.Success {
		t.Fatalf("valid split failed: %+v", resp)
	}
	body, _ := json.Marshal(envelope.ShowMemoryData{SessionID: gadgetSession, Path: "split/child"})
	response := h.Handle(context.Background(), &envelope.Command{Method: "show_memory", Data: body})
	shown := response.Data.(envelope.ShowMemoryResponse)
	if shown.Node == nil || shown.Node.Body != "durable" {
		t.Fatal("Hem/Qew viewer did not use gadget storage")
	}
	update, _ := json.Marshal(envelope.UpdateMemoryData{SessionID: gadgetSession, Path: "split/child", Body: "operator edit"})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "update_memory", Data: update}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("operator memory update failed: %+v", resp)
	}
	result := gadgetCall(t, params, "memory.get", map[string]string{"path": "split/child"})
	encoded, _ := json.Marshal(result.Data)
	if !strings.Contains(string(encoded), "operator edit") {
		t.Fatal("gadget did not see operator edit")
	}
	if n, _ := memory.Get(root, "split/child"); n.Revision != 2 {
		t.Fatalf("revision not incremented: %+v", n)
	}
	if _, err := os.Stat(filepath.Join(root, "split", "child", "README.md")); !os.IsNotExist(err) {
		t.Fatal("operation wrote retired memory files")
	}
	if count, _ := h.store.MemoryNodeCount(gadgetSession); count != 0 {
		t.Fatal("operation wrote memory into main database")
	}
}

func TestGadgetSchedulesAreOwnedAndRevocable(t *testing.T) {
	h, params := gadgetTestHandler(t)
	schedule, err := h.store.CreateSchedule(gadgetOtherSession, "other", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	requireGadgetError(t, gadgetCall(t, params, "schedule.delete", map[string]string{"id": jsonNumber(schedule)}), "not_found")
	if response := gadgetCall(t, params, "schedule.create", map[string]string{
		"at": time.Now().Add(time.Hour).Format(time.RFC3339), "prompt": "own",
	}); !response.Success {
		t.Fatalf("schedule create: %+v", response)
	}
	own, _ := h.store.ListSchedules(gadgetSession, "")
	if len(own) != 1 {
		t.Fatal("schedule not bound to session")
	}
	if resp := gadgetCall(t, params, "schedule.delete", map[string]string{"id": jsonNumber(own[0].ID)}); !resp.Success {
		t.Fatalf("own schedule delete: %+v", resp)
	}
	before, _ := h.store.ListSchedules(gadgetSession, "")
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	h.parseAndCreateSchedules(gadgetSession, `<schedule at="+1h">legacy bypass</schedule>`)
	after, _ := h.store.ListSchedules(gadgetSession, "")
	if len(after) != len(before) {
		t.Fatal("legacy output bypassed scheduling revocation")
	}
}

func jsonNumber(value int64) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func TestCapabilityProtocolPatchesPreserveOtherPermissions(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	setGadgetCaps(t, h, envelope.GadgetCapabilities{Scheduling: true, Traits: true})
	raw := json.RawMessage(`{"session_id":"` + gadgetSession + `","gadget_capabilities":{"agents":true}}`)
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: raw}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("partial permission update failed: %+v", resp)
	}
	caps, err := h.gadgetCapabilities(gadgetSession)
	if err != nil || caps != (envelope.GadgetCapabilities{Agents: true, Traits: true, Scheduling: true}) {
		t.Fatalf("partial update changed omitted fields: %+v, %v", caps, err)
	}
	for _, object := range []string{`{"typo":true}`, `{"memory":null}`, `{"memory":"false"}`, `{"traits":null}`, `{"traits":"true"}`} {
		raw := json.RawMessage(`{"session_id":"` + gadgetSession + `","gadget_capabilities":` + object + `}`)
		if resp := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: raw}); resp.Status != envelope.StatusError {
			t.Fatalf("invalid permission object accepted: %s", object)
		}
	}
	encoded, err := patchedGadgetCapabilities(json.RawMessage(`{"gadget_capabilities":{"agents":true}}`), envelope.DefaultGadgetCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	var defaults envelope.GadgetCapabilities
	_ = json.Unmarshal([]byte(encoded), &defaults)
	if !defaults.Memory || !defaults.Subagents || !defaults.Agents || !defaults.Scheduling {
		t.Fatal("create permission patch lost defaults")
	}
	if defaults.Traits {
		t.Fatal("create enabled traits without an explicit grant")
	}
}

func TestExistingSessionRouteRefreshFromOperatorOnly(t *testing.T) {
	h, params := gadgetTestHandler(t)
	route := map[string]string{"JAMES_HEM_ADDRESS": "relay/control", "JAMES_HEM_FINGERPRINT": "SHA256:operator"}
	body, _ := json.Marshal(envelope.SessionIDData{SessionID: gadgetSession, GadgetRoute: route})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "get_session", Data: body}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("operator route refresh failed: %+v", resp)
	}
	session, _ := h.store.GetSession(gadgetSession)
	env, _ := sessionEnvironment(session)
	storedRoute, _ := gadgetRoute(session.GadgetRoute)
	if env["KEEP"] != "configured" || env["JAMES_HEM_ADDRESS"] != "" || env["JAMES_HEM_SOCKET"] != "" ||
		storedRoute["JAMES_HEM_ADDRESS"] != "relay/control" || storedRoute["JAMES_HEM_SOCKET"] != "" {
		t.Fatal("route refresh did not separate internal route from agent environment")
	}
	requireGadgetError(t, gadgetCall(t, params, "memory.get", map[string]any{"gadget_route": route}), "invalid_request")
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if params.Environment["JAMES_HEM_ADDRESS"] != "" {
		t.Fatal("operator routing metadata reached agent process")
	}
}

func TestGadgetPromptShowsOnlyGrantedCommands(t *testing.T) {
	h, params := gadgetTestHandler(t)
	if strings.Contains(params.SystemPrompt, "gadgets agents list") || !strings.Contains(params.SystemPrompt, "gadgets memory get") {
		t.Fatal("default prompt does not match capability defaults")
	}

	setGadgetCaps(t, h, envelope.GadgetCapabilities{Agents: true})
	params.SystemPrompt = "Base instructions.\nYou have access to agent orchestration using the old Hem CLI\nhem delete session any"
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"gadgets memory get", "gadgets subagents", "gadgets schedule", "hem delete", "<root-memory>"} {
		if strings.Contains(params.SystemPrompt, text) {
			t.Fatalf("revoked/stale instructions remain: %s", text)
		}
	}
	for _, text := range []string{"Base instructions.", "gadgets agents list", "gadgets notify"} {
		if !strings.Contains(params.SystemPrompt, text) {
			t.Fatalf("missing allowed instruction: %s", text)
		}
	}
}

func TestRetiredMemoryInstructionsAreNotAdvertised(t *testing.T) {
	for _, suffix := range []string{
		"\nYou have a persistent session memory managed by hem.\nhem update memory example",
		"\nYou have a persistent memory for this session.\nEdit legacy README files",
		"\n<session-memory>Read and edit README.md directly</session-memory>",
		"\n<gadgets>hem delete session anything</gadgets>",
	} {
		if got := stripManagedGadgetInstructions("Base instructions." + suffix); got != "Base instructions." {
			t.Fatalf("stale instructions retained: %q", got)
		}
	}
}
