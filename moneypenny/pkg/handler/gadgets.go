package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/memory"
)

type gadgetRequest struct {
	Method string          `json:"method"`
	Data   json.RawMessage `json:"data"`
}

type gadgetError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *gadgetError) Error() string { return e.Message }

type gadgetResponse struct {
	Success bool         `json:"success"`
	Data    any          `json:"data,omitempty"`
	Error   *gadgetError `json:"error,omitempty"`
}

func patchedGadgetCapabilities(raw json.RawMessage, base envelope.GadgetCapabilities) (string, error) {
	var request struct {
		Capabilities map[string]*bool `json:"gadget_capabilities"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return "", err
	}
	for name, value := range request.Capabilities {
		if value == nil {
			return "", fmt.Errorf("gadget capability %q must be true or false", name)
		}
		switch name {
		case "memory":
			base.Memory = *value
		case "subagents":
			base.Subagents = *value
		case "agents":
			base.Agents = *value
		case "create_agents":
			base.CreateAgents = *value
		case "edit_sessions":
			base.EditSessions = *value
		case "edit_own_session":
			base.EditOwnSession = *value
		case "edit_own_subagents":
			base.EditOwnSubagents = *value
		case "traits":
			base.Traits = *value
		case "scheduling":
			base.Scheduling = *value
		default:
			return "", fmt.Errorf("unknown gadget capability %q", name)
		}
	}
	encoded, err := json.Marshal(base)
	return string(encoded), err
}

func (h *Handler) gadgetCapabilities(sessionID string) (envelope.GadgetCapabilities, error) {
	s, err := h.store.GetSession(sessionID)
	if err != nil {
		return envelope.GadgetCapabilities{}, err
	}
	if s == nil {
		return envelope.GadgetCapabilities{}, fmt.Errorf("session not found")
	}
	caps := envelope.DefaultGadgetCapabilities()
	if s.GadgetCapabilities != "" {
		if err := json.Unmarshal([]byte(s.GadgetCapabilities), &caps); err != nil {
			return envelope.GadgetCapabilities{}, fmt.Errorf("invalid stored gadget capabilities: %w", err)
		}
	}
	return caps, nil
}

// StartGadgets binds only loopback. Credentials live in daemon memory and the
// child process environment, not editable configuration or injected prompts.
func (h *Handler) StartGadgets() error {
	h.gadgetsMu.Lock()
	defer h.gadgetsMu.Unlock()
	if h.gadgetsServer != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start gadgets listener: %w", err)
	}
	h.gadgetsURL = "http://" + listener.Addr().String() + "/gadgets"
	h.gadgetsTokens = make(map[[32]byte]string)
	h.gadgetsSessionTokens = make(map[string]string)
	h.gadgetsServer = &http.Server{
		Handler: http.HandlerFunc(h.serveGadget), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	server := h.gadgetsServer
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.vlog("gadgets server: %v", err)
		}
	}()
	return nil
}

func (h *Handler) CloseGadgets() error {
	h.gadgetsMu.Lock()
	server := h.gadgetsServer
	h.gadgetsServer = nil
	h.gadgetsTokens = nil
	h.gadgetsSessionTokens = nil
	h.gadgetsURL = ""
	h.gadgetsMu.Unlock()
	if server != nil {
		return server.Close()
	}

	return nil
}

func (h *Handler) revokeGadgetToken(sessionID string) {
	h.gadgetsMu.Lock()
	defer h.gadgetsMu.Unlock()
	token := h.gadgetsSessionTokens[sessionID]
	delete(h.gadgetsTokens, sha256.Sum256([]byte(token)))
	delete(h.gadgetsSessionTokens, sessionID)
}

func (h *Handler) prepareGadgets(sessionID string, params *agent.RunParams) error {
	caps, err := h.gadgetCapabilities(sessionID)
	if err != nil {
		return err
	}
	session, err := h.store.GetSession(sessionID)
	if err != nil {
		return err
	}
	storedEnvironment, err := sessionEnvironment(session)
	if err != nil {
		return err
	}
	if err := h.StartGadgets(); err != nil {
		return err
	}
	h.gadgetsMu.Lock()
	token := h.gadgetsSessionTokens[sessionID]
	if token == "" {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			h.gadgetsMu.Unlock()
			return err
		}
		token = hex.EncodeToString(secret[:])
		h.gadgetsSessionTokens[sessionID] = token
		h.gadgetsTokens[sha256.Sum256([]byte(token))] = sessionID
	}
	url := h.gadgetsURL
	h.gadgetsMu.Unlock()
	env := make(map[string]string, len(storedEnvironment)+3)
	for key, value := range storedEnvironment {
		if !strings.HasPrefix(strings.ToUpper(key), "JAMES_HEM_") && !strings.HasPrefix(strings.ToUpper(key), "JAMES_GADGETS_") {
			env[key] = value
		}
	}
	env["JAMES_GADGETS_URL"], env["JAMES_GADGETS_TOKEN"] = url, token
	// Installers put gadgets beside the daemon; preserve configured PATH after
	// that directory, including Windows PATH's case-insensitive spelling.
	path := os.Getenv("PATH")
	for key, value := range env {
		if strings.EqualFold(key, "PATH") {
			path = value
			delete(env, key)
		}
	}
	if executable, err := os.Executable(); err == nil {
		env["PATH"] = filepath.Dir(executable) + string(os.PathListSeparator) + path
	}
	params.Environment = env
	params.SystemPrompt = stripManagedGadgetInstructions(params.SystemPrompt)
	params.SystemPrompt += "\n\n<gadgets>\nUse the gadgets executable for session tools. Identity is bound by the daemon; never supply another session ID or alter credentials. Commands return structured JSON and writes accept stdin. Permissions are checked for every request and may be revoked. Do not bypass tools with direct Hem commands, files, or database access.\n"
	params.SystemPrompt += "Reply to your parent: gadgets subagents message PARENT_ID (body on stdin); replying to your parent is always available.\n"
	if caps.Subagents {
		params.SystemPrompt += "Own subagents: gadgets subagents list; gadgets subagents create [--name name] [--agent agent] [--model model] [--path path] [--traits names-or-IDs]"
		if session.Yolo {
			params.SystemPrompt += " [--yolo]"
		}
		params.SystemPrompt += " (prompt on stdin); gadgets subagents message ID (body on stdin). Message only your direct children; creation inherits your permissions. Traits are comma-separated names or IDs; omitted or empty selects none."
		if session.Yolo {
			params.SystemPrompt += " You may request --yolo because this agent already has License to Kill."
		}
		params.SystemPrompt += "\n"
	}
	if caps.Agents {
		params.SystemPrompt += "All-agent discovery and messaging: gadgets agents list; gadgets agents message ID (body on stdin). This does not grant session editing or deletion.\n"
	}
	if caps.EditSessions || caps.EditOwnSession {
		params.SystemPrompt += "Session editing: gadgets sessions edit [SESSION_ID] [--name name] [--system-prompt text] [--model model] [--effort value] [--context tier] [--path path] [--compaction mode] [--yolo=true|false]. "
		if caps.EditSessions {
			params.SystemPrompt += "You may edit any tracked session."
		} else {
			params.SystemPrompt += "You may edit only your authenticated session."
		}
		params.SystemPrompt += " Gadget permissions and trusted routing cannot be changed.\n"
	}
	if caps.EditOwnSubagents {
		params.SystemPrompt += "Own subagent management: gadgets subagents edit ID [session fields], gadgets subagents complete ID, gadgets subagents stop ID, and gadgets subagents delete ID. These operations are restricted to your direct children.\n"
	}
	if caps.CreateAgents {
		params.SystemPrompt += "Create independent top-level agents: gadgets agents create [--name name] [--agent agent] [--model model] [--path path] [--traits names-or-IDs]"
		if session.Yolo {
			params.SystemPrompt += " [--yolo]"
		}
		params.SystemPrompt += " (prompt on stdin). Creation uses your moneypenny and inherits your current gadget permissions. Traits are comma-separated names or IDs; omitted applies Hem defaults, empty selects none. The initial prompt is automatically attributed to you; do not supply --from. This does not grant discovery, messaging, or management access."
		if session.Yolo {
			params.SystemPrompt += " You may request --yolo because this agent already has License to Kill."
		}
		params.SystemPrompt += "\n"
	}
	if caps.Traits {
		params.SystemPrompt += "Shared trait definitions: gadgets traits list; gadgets traits get ID; gadgets traits edit ID (complete replacement body on stdin; empty stdin clears it). IDs or exact names are accepted. You may edit only traits assigned to your own session; edits affect future use by all agents, not already-injected session prompts. No trait creation, deletion, assignment, renaming, or default changes are permitted.\n"
	}
	if caps.Scheduling {
		params.SystemPrompt += "Your scheduled prompts: gadgets schedule list; gadgets schedule create (--at RFC3339 | --cron 'five fields') --prompt 'task'; gadgets schedule delete ID. These commands act only on your session.\n"
	}
	params.SystemPrompt += "Operator notifications are always available: gadgets notify 'action needed' (or supply text on stdin). Use for actionable updates, not routine progress.\n</gadgets>"
	params.SystemPrompt += notifyUserSystemPromptSuffix
	return nil
}

func stripManagedGadgetInstructions(prompt string) string {
	for _, tag := range []string{"gadgets", "session-memory"} {
		for {
			start := strings.Index(prompt, "<"+tag+">")
			if start < 0 {
				break
			}
			end := strings.Index(prompt[start:], "</"+tag+">")
			if end < 0 {
				prompt = prompt[:start]
				break
			}
			prompt = prompt[:start] + prompt[start+end+len(tag)+3:]
		}
	}
	for _, marker := range []string{
		"You have access to agent orchestration using the",
		"You have a persistent session memory managed by hem.",
		"You have a persistent memory for this session.",
	} {
		if i := strings.Index(prompt, marker); i >= 0 {
			prompt = prompt[:i]
		}
	}
	return strings.TrimSpace(strings.ReplaceAll(prompt, notifyUserSystemPromptSuffix, ""))
}

func (h *Handler) serveGadget(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	reply := func(status int, data any, err error) {
		response := gadgetResponse{Success: err == nil, Data: data}
		if err != nil {
			response.Data = nil
			response.Error = &gadgetError{Code: "operation_failed", Message: err.Error()}
			var typed *gadgetError
			if errors.As(err, &typed) {
				response.Error = typed
			}
		}
		body, marshalErr := json.Marshal(response)
		if marshalErr != nil || len(body) > 4<<20 {
			status = http.StatusInternalServerError
			body = []byte(`{"success":false,"error":{"code":"response_too_large","message":"Response unavailable or exceeds 4 MiB; browse smaller branches"}}`)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	if r.Method != http.MethodPost || r.URL.Path != "/gadgets" || r.Header.Get("Origin") != "" {
		reply(http.StatusBadRequest, nil, &gadgetError{"invalid_request", "Use a local non-browser POST to /gadgets"})
		return
	}
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	h.gadgetsMu.Lock()
	sessionID := h.gadgetsTokens[sha256.Sum256([]byte(token))]
	h.gadgetsMu.Unlock()
	if !ok || token == "" || sessionID == "" {
		reply(http.StatusUnauthorized, nil, &gadgetError{"unauthorized", "Invalid session credential"})
		return
	}
	var request gadgetRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		reply(http.StatusBadRequest, nil, &gadgetError{"invalid_request", err.Error()})
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		reply(http.StatusBadRequest, nil, &gadgetError{"invalid_request", "Expected one JSON request"})
		return
	}
	data, err := h.executeGadget(r.Context(), sessionID, request)
	reply(http.StatusOK, data, err)
}

func decodeGadget(data json.RawMessage, target any) error {
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return &gadgetError{"invalid_request", err.Error()}
	}
	return nil
}

func (h *Handler) executeGadget(ctx context.Context, sessionID string, request gadgetRequest) (any, error) {
	caps, err := h.gadgetCapabilities(sessionID)
	if err != nil {
		return nil, &gadgetError{"unauthorized", "Session credential is no longer valid"}
	}
	allowed := false
	switch request.Method {
	case "memory.get", "memory.list", "memory.search", "memory.set", "memory.batch", "memory.delete", "memory.revisions":
		allowed = caps.Memory
	case "agents.list", "agents.message":
		allowed = caps.Agents
	case "agents.create":
		allowed = caps.CreateAgents
	case "sessions.edit":
		allowed = caps.EditSessions || caps.EditOwnSession
	case "traits.list", "traits.get", "traits.edit":
		allowed = caps.Traits
	case "subagents.list", "subagents.create":
		allowed = caps.Subagents
	case "subagents.message":
		allowed = true
	case "subagents.edit", "subagents.complete", "subagents.stop", "subagents.delete":
		allowed = caps.EditOwnSubagents
	case "schedule.list", "schedule.create", "schedule.delete":
		allowed = caps.Scheduling
	case "notify":
		allowed = true
	default:
		return nil, &gadgetError{"unknown_method", "Unknown gadget method"}
	}
	if !allowed {
		return nil, &gadgetError{"permission_denied", "Capability is disabled for this session"}
	}
	if strings.HasPrefix(request.Method, "memory.") {
		return h.memoryGadget(sessionID, request)
	}
	if strings.HasPrefix(request.Method, "traits.") {
		if _, err := envelope.DecodeTraitGadget(request.Method, request.Data); err != nil {
			return nil, &gadgetError{"invalid_request", err.Error()}
		}
	}
	if request.Method == "agents.create" || request.Method == "subagents.create" {
		if _, err := envelope.DecodeCreateAgentGadget(request.Data); err != nil {
			return nil, &gadgetError{"invalid_request", err.Error()}
		}
	}
	if strings.HasPrefix(request.Method, "agents.") || strings.HasPrefix(request.Method, "subagents.") || strings.HasPrefix(request.Method, "traits.") {
		return h.routeGadget(ctx, sessionID, request.Method, request.Data)
	}
	if strings.HasPrefix(request.Method, "schedule.") {
		return h.scheduleGadget(ctx, sessionID, request)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := decodeGadget(request.Data, &data); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(data.Text)
	if text == "" || utf8.RuneCountInString(text) > 1000 {
		return nil, &gadgetError{"invalid_request", "Notification must contain 1–1000 Unicode characters"}
	}
	if err := h.store.AddConversationTurn(sessionID, "notification", text); err != nil {
		return nil, err
	}
	_ = h.notifyWriter.Send(envelope.EventChatUserNotification, sessionID, map[string]string{"message": text})
	return map[string]bool{"notified": true}, nil
}

func (h *Handler) memoryGadget(sessionID string, request gadgetRequest) (any, error) {
	if err := h.MigrateSessionMemoryToSQLite(sessionID); err != nil {
		return nil, &gadgetError{"migration_required", err.Error()}
	}
	root := h.memoryDir(sessionID)
	switch request.Method {
	case "memory.get", "memory.list", "memory.revisions":
		var data struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  *int   `json:"limit"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		if request.Method == "memory.list" {
			nodes, err := memory.Children(root, data.Path)
			if err != nil {
				return nil, err
			}
			// Browsing is metadata-only even for imported oversized notes.
			out := make([]map[string]any, 0, len(nodes))
			for _, n := range nodes {
				out = append(out, map[string]any{"path": n.Path, "description": n.Description, "revision": n.Revision, "characters": utf8.RuneCountInString(n.Body)})
			}
			return out, nil
		}
		limit := memory.MaxReadCharacters
		if data.Limit != nil {
			limit = *data.Limit
		}
		node, err := memory.Read(root, data.Path, data.Offset, limit)
		if err != nil {
			return nil, err
		}
		if node == nil {
			return nil, &gadgetError{"not_found", "Memory node not found"}
		}
		if request.Method == "memory.revisions" {
			return map[string]any{"path": node.Path, "revision": node.Revision}, nil
		}
		return node, nil
	case "memory.search":
		var data struct {
			Query string `json:"query"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		return memory.Search(root, data.Query)
	case "memory.set":
		var data struct {
			Path string  `json:"path"`
			Body *string `json:"body"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		if data.Body == nil {
			return nil, &gadgetError{"invalid_request", "body is required"}
		}
		path, err := memory.Set(root, data.Path, *data.Body)
		return map[string]string{"path": path}, err
	case "memory.batch":
		var data struct {
			Entries []struct {
				Path string  `json:"path"`
				Body *string `json:"body"`
			} `json:"entries"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		if len(data.Entries) == 0 {
			return nil, &gadgetError{"invalid_request", "entries must not be empty"}
		}
		nodes := make([]*memory.Node, 0, len(data.Entries))
		for _, entry := range data.Entries {
			if entry.Body == nil {
				return nil, &gadgetError{"invalid_request", "Every batch entry requires body"}
			}
			nodes = append(nodes, &memory.Node{Path: entry.Path, Body: *entry.Body})
		}
		if err := memory.SetBatch(root, nodes); err != nil {
			return nil, err
		}
		return map[string]int{"updated": len(data.Entries)}, nil
	case "memory.delete":
		var data struct {
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
		}
		if err := decodeGadget(request.Data, &data); err != nil {
			return nil, err
		}
		count, err := memory.Delete(root, data.Path, data.Recursive)
		return map[string]int{"deleted": count}, err
	}
	return nil, &gadgetError{"unknown_method", "Unknown memory operation"}
}

func gadgetEnvelope(response *envelope.Response) (any, error) {
	if response.Status == envelope.StatusError {
		message := "Daemon operation failed"
		if data, ok := response.Data.(map[string]string); ok {
			message = data["message"]
		}
		return nil, &gadgetError{response.ErrorCode, message}
	}
	return response.Data, nil
}

func (h *Handler) scheduleGadget(ctx context.Context, sessionID string, request gadgetRequest) (any, error) {
	var data struct {
		ID     string `json:"id"`
		At     string `json:"at"`
		Cron   string `json:"cron"`
		Prompt string `json:"prompt"`
	}
	if err := decodeGadget(request.Data, &data); err != nil {
		return nil, err
	}
	command := func(method string, payload any) *envelope.Command {
		body, _ := json.Marshal(payload)
		return &envelope.Command{Method: method, Data: body}
	}
	switch request.Method {
	case "schedule.list":
		return gadgetEnvelope(h.listSchedules(ctx, command("list_schedules", envelope.ListSchedulesData{SessionID: sessionID})))
	case "schedule.create":
		if (data.At == "") == (data.Cron == "") {
			return nil, &gadgetError{"invalid_request", "Supply exactly one of at or cron"}
		}
		if data.Cron != "" {
			next, err := nextCronTime(data.Cron, time.Now())
			if err != nil {
				return nil, &gadgetError{"invalid_request", err.Error()}
			}
			data.At = next.Format(time.RFC3339)
		}
		return gadgetEnvelope(h.schedule(ctx, command("schedule", envelope.ScheduleData{
			SessionID: sessionID, ScheduledAt: data.At, CronExpr: data.Cron, Prompt: data.Prompt,
		})))
	case "schedule.delete":
		id, err := strconv.ParseInt(data.ID, 10, 64)
		if err != nil || id < 1 {
			return nil, &gadgetError{"invalid_request", "Invalid schedule ID"}
		}
		schedule, err := h.store.GetSchedule(id)
		if err != nil || schedule == nil || schedule.SessionID != sessionID {
			return nil, &gadgetError{"not_found", "Schedule not found in this session"}
		}
		return gadgetEnvelope(h.cancelSchedule(ctx, command("cancel_schedule", envelope.CancelScheduleData{ScheduleID: id})))
	}
	return nil, &gadgetError{"unknown_method", "Unknown schedule operation"}
}
