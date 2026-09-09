package handler

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"james/moneypenny/pkg/envelope"
)

// Refresh routes through the trusted operator transport, including existing
// sessions first opened/continued after an upgrade. Agent gadget requests
// cannot call Handle or supply this field.
func (h *Handler) refreshGadgetRoute(cmd *envelope.Command) error {
	switch cmd.Method {
	case "get_session", "continue_session", "queue_prompt", "compact_session", "distill_session":
	default:
		return nil
	}
	var data struct {
		SessionID string            `json:"session_id"`
		Route     map[string]string `json:"gadget_route"`
	}
	if err := json.Unmarshal(cmd.Data, &data); err != nil {
		return err
	}
	if len(data.Route) == 0 {
		return nil
	}
	for key, value := range data.Route {
		switch key {
		case "JAMES_HEM_SOCKET", "JAMES_HEM_ADDRESS", "JAMES_HEM_FINGERPRINT":
		default:
			return fmt.Errorf("invalid gadget route field %q", key)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid gadget route value")
		}
	}
	session, err := h.store.GetSession(data.SessionID)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	env, err := sessionEnvironment(session)
	if err != nil {
		return err
	}
	if env == nil {
		env = make(map[string]string)
	}
	changed := false
	for _, key := range []string{"JAMES_HEM_SOCKET", "JAMES_HEM_ADDRESS", "JAMES_HEM_FINGERPRINT"} {
		if env[key] != data.Route[key] {
			changed = true
		}
		delete(env, key)
	}
	for key, value := range data.Route {
		env[key] = value
	}
	if !changed {
		return nil
	}
	encoded, err := validateEnvironment(env)
	if err != nil {
		return err
	}
	return h.store.UpdateSessionFields(data.SessionID, nil, nil, nil, nil, nil, nil, nil, &encoded, nil)
}

// These wire types deliberately keep the daemon independent of the Hem module.
type gadgetHemRequest struct {
	Verb      string   `json:"verb"`
	Noun      string   `json:"noun"`
	Args      []string `json:"args"`
	RequestID string   `json:"request_id"`
}

type gadgetHemResponse struct {
	Status    string          `json:"status"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

func (h *Handler) routeGadget(ctx context.Context, sessionID, method string, data json.RawMessage) (any, error) {
	switch method {
	case "agents.list", "agents.message", "subagents.list", "subagents.create", "subagents.message":
	default:
		return nil, fmt.Errorf("unsupported routed gadget method %q", method)
	}
	session, err := h.store.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("gadget source session: %w", err)
	}
	if session == nil {
		return nil, fmt.Errorf("gadget source session not found")
	}
	environment, err := sessionEnvironment(session)
	if err != nil {
		return nil, err
	}
	// Source identity is never accepted from gadget request data or process env.
	payload, err := json.Marshal(struct {
		SourceSessionID string          `json:"source_session_id"`
		Method          string          `json:"method"`
		Data            json.RawMessage `json:"data"`
	}{sessionID, method, data})
	if err != nil {
		return nil, fmt.Errorf("encoding gadget request: %w", err)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	request := gadgetHemRequest{"gadget", "route", []string{string(payload)}, "gadget-" + hex.EncodeToString(id[:])}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	address, socket := environment["JAMES_HEM_ADDRESS"], environment["JAMES_HEM_SOCKET"]
	if address != "" {
		fingerprint := environment["JAMES_HEM_FINGERPRINT"]
		if fingerprint == "" {
			return nil, fmt.Errorf("gadget Hem route requires JAMES_HEM_FINGERPRINT")
		}
		return h.routeGadgetMI6(ctx, address, fingerprint, request)
	}
	if socket == "" {
		return nil, fmt.Errorf("gadget Hem route is not configured; update the session from its Hem server")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connecting gadget Hem socket: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, fmt.Errorf("sending gadget Hem request: %w", err)
	}
	return readGadgetHemResponse(conn, request.RequestID)
}

func (h *Handler) routeGadgetMI6(ctx context.Context, address, fingerprint string, request gadgetHemRequest) (any, error) {
	clientName := "mi6-client"
	if runtime.GOOS == "windows" {
		clientName += ".exe"
	}
	client, err := exec.LookPath(clientName)
	if err != nil {
		if executable, exeErr := os.Executable(); exeErr == nil {
			client = filepath.Join(filepath.Dir(executable), clientName)
		} else {
			return nil, fmt.Errorf("mi6-client is required for gadget Hem routing: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Use the daemon's existing relay identity, never an agent-provided key.
	cmd := exec.CommandContext(ctx, client, "--line-mode", "--key", filepath.Join(h.dataDir, "moneypenny_ecdsa"),
		"--server-fingerprint", fingerprint, address)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("starting gadget relay client: %w", err)
	}
	defer func() {
		cancel()
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		return nil, fmt.Errorf("sending gadget relay request: %w", err)
	}
	return readGadgetHemResponse(stdout, request.RequestID)
}

func readGadgetHemResponse(reader io.Reader, requestID string) (any, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var response gadgetHemResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			return nil, fmt.Errorf("invalid gadget Hem response: %w", err)
		}
		// MI6 also carries broadcasts and other clients' requests/responses.
		if response.RequestID != requestID || response.Status == "" {
			continue
		}
		switch response.Status {
		case "ok":
			if len(response.Data) == 0 {
				return nil, fmt.Errorf("gadget Hem response is missing data")
			}
			return response.Data, nil
		case "error":
			return nil, fmt.Errorf("gadget Hem request failed: %s", response.Message)
		default:
			return nil, fmt.Errorf("invalid gadget Hem response status %q", response.Status)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading gadget Hem response: %w", err)
	}
	return nil, fmt.Errorf("gadget Hem connection closed without a matching response")
}
