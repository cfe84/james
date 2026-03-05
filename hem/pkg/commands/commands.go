package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/hem/pkg/transport"

	"crypto/rand"
)

// Executor runs commands using the store and transport layer.
type Executor struct {
	store      *store.Store
	mi6KeyPath string
}

func New(s *store.Store, mi6KeyPath string) *Executor {
	return &Executor{store: s, mi6KeyPath: mi6KeyPath}
}

// Dispatch routes a verb+noun+args to the appropriate handler.
func (e *Executor) Dispatch(verb, noun string, args []string) *protocol.Response {
	switch verb + " " + noun {
	// Moneypenny commands
	case "add moneypenny":
		return e.AddMoneypenny(args)
	case "list moneypenny":
		return e.ListMoneypennies(args)
	case "ping moneypenny":
		return e.PingMoneypenny(args)
	case "delete moneypenny":
		return e.DeleteMoneypenny(args)
	case "set-default moneypenny":
		return e.SetDefaultMoneypenny(args)
	case "set-default agent":
		return e.SetDefaultValue("agent", args)
	case "set-default path":
		return e.SetDefaultValue("path", args)
	case "get-default moneypenny", "get-default agent", "get-default path":
		return e.GetDefaultValue(noun)
	case "list default":
		return e.ListDefaults(args)

	// Session commands
	case "create session":
		return e.CreateSession(args)
	case "continue session":
		return e.ContinueSession(args)
	case "stop session":
		return e.StopSession(args)
	case "delete session":
		return e.DeleteSession(args)
	case "state session":
		return e.StateSession(args)
	case "last session":
		return e.LastSession(args)
	case "show session":
		return e.ShowSession(args)
	case "history session", "log session":
		return e.HistorySession(args)
	case "list session":
		return e.ListSessions(args)

	default:
		return protocol.ErrResponse(fmt.Sprintf("unknown command: %s %s", verb, noun))
	}
}

// generateSessionID creates a UUID v4.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("failed to generate session ID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// clientForMoneypenny creates a transport.Client for the given moneypenny.
func (e *Executor) clientForMoneypenny(mp *store.Moneypenny) *transport.Client {
	switch mp.TransportType {
	case store.TransportFIFO:
		return transport.NewFIFOClient(mp.FIFOIn, mp.FIFOOut)
	case store.TransportMI6:
		return transport.NewMI6Client(mp.MI6Addr, e.mi6KeyPath)
	default:
		return nil
	}
}

// sendCommand sends a command to a moneypenny and returns the response.
func (e *Executor) sendCommand(ctx context.Context, mp *store.Moneypenny, method string, data interface{}) (*transport.Response, error) {
	client := e.clientForMoneypenny(mp)
	if client == nil {
		return nil, fmt.Errorf("unsupported transport type %q for moneypenny %q", mp.TransportType, mp.Name)
	}
	cmd := &transport.Command{
		Type:      "request",
		Method:    method,
		RequestID: generateSessionID(),
		Data:      data,
	}
	resp, err := client.Send(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("sending %s to %q: %w", method, mp.Name, err)
	}
	if resp.Status == "error" {
		return resp, fmt.Errorf("moneypenny %q returned error: %s (code: %s)", mp.Name, string(resp.Data), resp.ErrorCode)
	}
	return resp, nil
}

// resolveSessionMoneypenny looks up which moneypenny a session belongs to.
func (e *Executor) resolveSessionMoneypenny(sessionID string) (*store.Moneypenny, error) {
	mpName, err := e.store.GetSessionMoneypenny(sessionID)
	if err != nil {
		return nil, fmt.Errorf("looking up session %q: %w", sessionID, err)
	}
	if mpName == "" {
		return nil, fmt.Errorf("session %q not found in local tracking", sessionID)
	}
	mp, err := e.store.GetMoneypenny(mpName)
	if err != nil {
		return nil, fmt.Errorf("getting moneypenny %q: %w", mpName, err)
	}
	if mp == nil {
		return nil, fmt.Errorf("moneypenny %q (for session %q) not found", mpName, sessionID)
	}
	return mp, nil
}

// parseFlagsFromArgs creates a new FlagSet, applies the setup function to register flags,
// parses the args, and returns remaining non-flag args.
func parseFlagsFromArgs(name string, args []string, setup func(fs *flag.FlagSet)) ([]string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	setup(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

// moneypennyAddress returns a display address for a moneypenny.
func moneypennyAddress(mp *store.Moneypenny) string {
	switch mp.TransportType {
	case store.TransportFIFO:
		dir := filepath.Dir(mp.FIFOIn)
		if filepath.Dir(mp.FIFOOut) == dir {
			return dir
		}
		return mp.FIFOIn + " / " + mp.FIFOOut
	case store.TransportMI6:
		return mp.MI6Addr
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Result types for structured responses
// ---------------------------------------------------------------------------

type TextResult struct {
	Message string `json:"message"`
}

type TableResult struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

type SessionCreatedResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response,omitempty"`
	Async     bool   `json:"async"`
}

type SessionContinuedResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response,omitempty"`
	Async     bool   `json:"async"`
}

type SessionStateResult struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type SessionLastResult struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
}

type SessionShowResult struct {
	SessionID    string `json:"session_id"`
	Moneypenny   string `json:"moneypenny"`
	Name         string `json:"name,omitempty"`
	Agent        string `json:"agent,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Yolo         bool   `json:"yolo,omitempty"`
	Path         string `json:"path,omitempty"`
	Status       string `json:"status,omitempty"`
}

type ConversationTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type HistoryResult struct {
	SessionID    string             `json:"session_id"`
	Conversation []ConversationTurn `json:"conversation"`
}

// ---------------------------------------------------------------------------
// Moneypenny commands
// ---------------------------------------------------------------------------

func (e *Executor) AddMoneypenny(args []string) *protocol.Response {
	var name, fifoFolder, fifoIn, fifoOut, mi6Addr string

	remaining, err := parseFlagsFromArgs("add-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
		fs.StringVar(&fifoFolder, "fifo-folder", "", "folder containing moneypenny-in and moneypenny-out FIFOs")
		fs.StringVar(&fifoIn, "fifo-in", "", "path to moneypenny input FIFO")
		fs.StringVar(&fifoOut, "fifo-out", "", "path to moneypenny output FIFO")
		fs.StringVar(&mi6Addr, "mi6", "", "MI6 address")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	_ = remaining

	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	hasFIFOFolder := fifoFolder != ""
	hasFIFOPaths := fifoIn != "" || fifoOut != ""
	hasMI6 := mi6Addr != ""

	specCount := 0
	if hasFIFOFolder {
		specCount++
	}
	if hasFIFOPaths {
		specCount++
	}
	if hasMI6 {
		specCount++
	}
	if specCount != 1 {
		return protocol.ErrResponse("exactly one transport must be specified: --fifo-folder, --fifo-in/--fifo-out, or --mi6")
	}

	mp := &store.Moneypenny{
		Name:      name,
		CreatedAt: time.Now(),
	}

	switch {
	case hasFIFOFolder:
		mp.TransportType = store.TransportFIFO
		mp.FIFOIn = filepath.Join(fifoFolder, "moneypenny-in")
		mp.FIFOOut = filepath.Join(fifoFolder, "moneypenny-out")
	case hasFIFOPaths:
		if fifoIn == "" || fifoOut == "" {
			return protocol.ErrResponse("both --fifo-in and --fifo-out are required when specifying FIFO paths directly")
		}
		mp.TransportType = store.TransportFIFO
		mp.FIFOIn = fifoIn
		mp.FIFOOut = fifoOut
	case hasMI6:
		mp.TransportType = store.TransportMI6
		mp.MI6Addr = mi6Addr
	}

	if err := e.store.AddMoneypenny(mp); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("storing moneypenny: %v", err))
	}

	// Validate connectivity by sending get_version.
	client := e.clientForMoneypenny(mp)
	pingCmd := &transport.Command{
		Type:      "request",
		Method:    "get_version",
		RequestID: "ping",
		Data:      nil,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Send(ctx, pingCmd)
	if err != nil {
		_ = e.store.DeleteMoneypenny(name)
		return protocol.ErrResponse(fmt.Sprintf("connectivity check failed for %q: %v", name, err))
	}
	if resp.Status == "error" {
		_ = e.store.DeleteMoneypenny(name)
		return protocol.ErrResponse(fmt.Sprintf("connectivity check failed for %q: moneypenny returned error: %s", name, string(resp.Data)))
	}

	var versionData struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp.Data, &versionData); err != nil {
		versionData.Version = string(resp.Data)
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Added moneypenny %q (%s). Version: %s", name, mp.TransportType, versionData.Version),
	})
}

func (e *Executor) ListMoneypennies(args []string) *protocol.Response {
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	result := TableResult{
		Headers: []string{"Name", "Type", "Address", "Default"},
	}
	for _, mp := range mps {
		def := ""
		if mp.IsDefault {
			def = "*"
		}
		result.Rows = append(result.Rows, []string{
			mp.Name,
			mp.TransportType,
			moneypennyAddress(mp),
			def,
		})
	}

	return protocol.OKResponse(result)
}

func (e *Executor) PingMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("ping-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	mp, err := e.store.GetMoneypenny(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", name))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := e.sendCommand(ctx, mp, "get_version", nil)
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("ping failed: %v", err))
	}

	var versionData struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp.Data, &versionData); err != nil {
		versionData.Version = string(resp.Data)
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Moneypenny %q is reachable. Version: %s", name, versionData.Version),
	})
}

func (e *Executor) DeleteMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("delete-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	mp, err := e.store.GetMoneypenny(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", name))
	}

	if err := e.store.DeleteMoneypenny(name); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Deleted moneypenny %q (and its tracked sessions).", name),
	})
}

func (e *Executor) SetDefaultMoneypenny(args []string) *protocol.Response {
	var name string
	_, err := parseFlagsFromArgs("set-default-moneypenny", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "n", "", "moneypenny name")
		fs.StringVar(&name, "name", "", "moneypenny name")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if name == "" {
		return protocol.ErrResponse("--name / -n is required")
	}

	if err := e.store.SetDefaultMoneypenny(name); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Default moneypenny set to %q.", name),
	})
}

// SetDefaultValue sets a default value (agent, path).
func (e *Executor) SetDefaultValue(key string, args []string) *protocol.Response {
	if len(args) == 0 {
		return protocol.ErrResponse(fmt.Sprintf("value is required: hem set-default %s VALUE", key))
	}
	value := args[0]

	if err := e.store.SetDefault(key, value); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Default %s set to %q.", key, value),
	})
}

// GetDefaultValue returns a default value.
func (e *Executor) GetDefaultValue(key string) *protocol.Response {
	value, err := e.store.GetDefault(key)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if value == "" {
		return protocol.OKResponse(TextResult{
			Message: fmt.Sprintf("No default %s set.", key),
		})
	}
	return protocol.OKResponse(TextResult{
		Message: value,
	})
}

// ListDefaults lists all defaults.
func (e *Executor) ListDefaults(args []string) *protocol.Response {
	defaults, err := e.store.ListDefaults()
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Also include the moneypenny default from its own table.
	mp, err := e.store.GetDefaultMoneypenny()
	if err == nil && mp != nil {
		defaults["moneypenny"] = mp.Name
	}

	result := TableResult{
		Headers: []string{"Key", "Value"},
	}
	for k, v := range defaults {
		result.Rows = append(result.Rows, []string{k, v})
	}

	return protocol.OKResponse(result)
}

// ---------------------------------------------------------------------------
// Session commands
// ---------------------------------------------------------------------------

func (e *Executor) CreateSession(args []string) *protocol.Response {
	var mpName, sessionName, systemPrompt, pathArg, agentName string
	var yolo, async bool

	remaining, err := parseFlagsFromArgs("create-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
		fs.StringVar(&agentName, "agent", "", "agent to use")
		fs.StringVar(&sessionName, "name", "", "session name")
		fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt")
		fs.BoolVar(&yolo, "yolo", false, "enable yolo mode")
		fs.StringVar(&pathArg, "path", "", "working directory path")
		fs.BoolVar(&async, "async", false, "return immediately without waiting for response")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	// Apply stored defaults for agent and path when not specified.
	if agentName == "" {
		if v, _ := e.store.GetDefault("agent"); v != "" {
			agentName = v
		} else {
			agentName = "claude"
		}
	}
	if pathArg == "" {
		if v, _ := e.store.GetDefault("path"); v != "" {
			pathArg = v
		} else {
			pathArg = "."
		}
	}

	var mp *store.Moneypenny
	if mpName != "" {
		mp, err = e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
	} else {
		mp, err = e.store.GetDefaultMoneypenny()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse("no moneypenny specified and no default set")
		}
	}

	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		return protocol.ErrResponse("prompt is required (pass as trailing arguments)")
	}

	sessionID := generateSessionID()

	cmdData := map[string]interface{}{
		"agent":      agentName,
		"session_id": sessionID,
		"name":       sessionName,
		"prompt":     prompt,
		"path":       pathArg,
	}
	if systemPrompt != "" {
		cmdData["system_prompt"] = systemPrompt
	}
	if yolo {
		cmdData["yolo"] = true
	}

	if err := e.store.TrackSession(sessionID, mp.Name); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("tracking session: %v", err))
	}

	if async {
		go func() {
			ctx := context.Background()
			e.sendCommand(ctx, mp, "create_session", cmdData)
		}()
		return protocol.OKResponse(SessionCreatedResult{
			SessionID: sessionID,
			Async:     true,
		})
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "create_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var respData struct {
		Response string `json:"response"`
	}
	responseText := string(resp.Data)
	if err := json.Unmarshal(resp.Data, &respData); err == nil && respData.Response != "" {
		responseText = respData.Response
	}

	return protocol.OKResponse(SessionCreatedResult{
		SessionID: sessionID,
		Response:  responseText,
	})
}

func (e *Executor) ContinueSession(args []string) *protocol.Response {
	var sessionID string
	var async bool

	remaining, err := parseFlagsFromArgs("continue-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.BoolVar(&async, "async", false, "return immediately without waiting for response")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
		remaining = remaining[1:]
	}

	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		return protocol.ErrResponse("prompt is required")
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
		"prompt":     prompt,
	}

	if async {
		go func() {
			ctx := context.Background()
			e.sendCommand(ctx, mp, "continue_session", cmdData)
		}()
		return protocol.OKResponse(SessionContinuedResult{
			SessionID: sessionID,
			Async:     true,
		})
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "continue_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var respData struct {
		Response string `json:"response"`
	}
	responseText := string(resp.Data)
	if err := json.Unmarshal(resp.Data, &respData); err == nil && respData.Response != "" {
		responseText = respData.Response
	}

	return protocol.OKResponse(SessionContinuedResult{
		SessionID: sessionID,
		Response:  responseText,
	})
}

func (e *Executor) StopSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("stop-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "stop_session", cmdData); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Session %s stopped.", sessionID),
	})
}

func (e *Executor) DeleteSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("delete-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	if _, err := e.sendCommand(ctx, mp, "delete_session", cmdData); err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if err := e.store.DeleteTrackedSession(sessionID); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("removing local session tracking: %v", err))
	}

	return protocol.OKResponse(TextResult{
		Message: fmt.Sprintf("Session %s deleted.", sessionID),
	})
}

func (e *Executor) StateSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("state-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var sessionData struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing session state: %v", err))
	}

	return protocol.OKResponse(SessionStateResult{
		SessionID: sessionID,
		Status:    sessionData.Status,
	})
}

func (e *Executor) LastSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("last-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var sessionData struct {
		Conversation []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"conversation"`
	}
	if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing session data: %v", err))
	}

	for i := len(sessionData.Conversation) - 1; i >= 0; i-- {
		if sessionData.Conversation[i].Role == "assistant" {
			return protocol.OKResponse(SessionLastResult{
				SessionID: sessionID,
				Response:  sessionData.Conversation[i].Text,
			})
		}
	}

	return protocol.OKResponse(SessionLastResult{
		SessionID: sessionID,
		Response:  "(no assistant response found)",
	})
}

func (e *Executor) ShowSession(args []string) *protocol.Response {
	var sessionID string

	remaining, err := parseFlagsFromArgs("show-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing session data: %v", err))
	}

	result := SessionShowResult{
		SessionID:  sessionID,
		Moneypenny: mp.Name,
	}
	if v, ok := raw["name"].(string); ok {
		result.Name = v
	}
	if v, ok := raw["agent"].(string); ok {
		result.Agent = v
	}
	if v, ok := raw["system_prompt"].(string); ok {
		result.SystemPrompt = v
	}
	if v, ok := raw["yolo"].(bool); ok {
		result.Yolo = v
	}
	if v, ok := raw["path"].(string); ok {
		result.Path = v
	}
	if v, ok := raw["status"].(string); ok {
		result.Status = v
	}

	return protocol.OKResponse(result)
}

func (e *Executor) HistorySession(args []string) *protocol.Response {
	var sessionID string
	var numTurns int

	remaining, err := parseFlagsFromArgs("history-session", args, func(fs *flag.FlagSet) {
		fs.StringVar(&sessionID, "session-id", "", "session ID")
		fs.IntVar(&numTurns, "n", 0, "number of turns to show (0 = all)")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	if sessionID == "" {
		if len(remaining) == 0 {
			return protocol.ErrResponse("session_id is required")
		}
		sessionID = remaining[0]
	}

	mp, err := e.resolveSessionMoneypenny(sessionID)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	cmdData := map[string]interface{}{
		"session_id": sessionID,
	}

	ctx := context.Background()
	resp, err := e.sendCommand(ctx, mp, "get_session", cmdData)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var sessionData struct {
		Conversation []ConversationTurn `json:"conversation"`
	}
	if err := json.Unmarshal(resp.Data, &sessionData); err != nil {
		return protocol.ErrResponse(fmt.Sprintf("parsing conversation: %v", err))
	}

	conv := sessionData.Conversation
	if numTurns > 0 && numTurns < len(conv) {
		conv = conv[len(conv)-numTurns:]
	}

	return protocol.OKResponse(HistoryResult{
		SessionID:    sessionID,
		Conversation: conv,
	})
}

func (e *Executor) ListSessions(args []string) *protocol.Response {
	var mpName string

	_, err := parseFlagsFromArgs("list-sessions", args, func(fs *flag.FlagSet) {
		fs.StringVar(&mpName, "m", "", "moneypenny name filter")
		fs.StringVar(&mpName, "moneypenny", "", "moneypenny name filter")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}

	var mps []*store.Moneypenny
	if mpName != "" {
		mp, err := e.store.GetMoneypenny(mpName)
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
		if mp == nil {
			return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", mpName))
		}
		mps = []*store.Moneypenny{mp}
	} else {
		mps, err = e.store.ListMoneypennies()
		if err != nil {
			return protocol.ErrResponse(err.Error())
		}
	}

	result := TableResult{
		Headers: []string{"SessionID", "Name", "Status", "Moneypenny"},
	}

	ctx := context.Background()
	for _, mp := range mps {
		resp, err := e.sendCommand(ctx, mp, "list_sessions", nil)
		if err != nil {
			continue
		}

		var sessions []struct {
			SessionID string `json:"session_id"`
			Name      string `json:"name"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &sessions); err != nil {
			continue
		}

		for _, s := range sessions {
			result.Rows = append(result.Rows, []string{s.SessionID, s.Name, s.Status, mp.Name})
		}
	}

	return protocol.OKResponse(result)
}
