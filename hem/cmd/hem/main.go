package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"james/hem/pkg/cli"
	"james/hem/pkg/commands"
	"james/hem/pkg/hemclient"
	"james/hem/pkg/output"
	"james/hem/pkg/protocol"
	"james/hem/pkg/server"
	"james/hem/pkg/store"
	"james/hem/pkg/ui"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Extract global flags from args before cli.Parse.
	var mi6Addr string
	var silent, verbose bool
	filteredArgs := make([]string, 0, len(os.Args))
	filteredArgs = append(filteredArgs, os.Args[0])
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--hem" && i+1 < len(os.Args) {
			i++
			mi6Addr = os.Args[i]
		} else if os.Args[i] == "--silent" {
			silent = true
		} else if os.Args[i] == "--verbose" {
			verbose = true
		} else {
			filteredArgs = append(filteredArgs, os.Args[i])
		}
	}
	os.Args = filteredArgs

	// Build sender based on transport.
	var sender hemclient.Sender
	sender = buildSender(mi6Addr)
	if verbose {
		sender = &verboseSender{inner: sender}
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Handle help and version flags before parsing.
	if os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		printUsage()
		return
	}
	if os.Args[1] == "--version" || os.Args[1] == "-v" {
		fmt.Printf("hem client: %s\n", Version)
		req := &protocol.Request{Verb: "get-version"}
		resp, err := sender.Send(req)
		if err != nil {
			fmt.Println("hem server: not running")
		} else {
			var result struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(resp.Data, &result) == nil {
				fmt.Printf("hem server: %s\n", result.Version)
			}
		}
		return
	}

	cmd, err := cli.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		printUsage()
		os.Exit(1)
	}

	// Handle local-only commands that don't need the server.
	switch cmd.Verb {
	case "version":
		fmt.Println(Version)
		return
	case "show-public-key":
		dataDir := defaultDataDir()
		os.MkdirAll(dataDir, 0700)
		keyPath := filepath.Join(dataDir, "hem_ecdsa")
		pubKey, err := loadOrCreatePublicKey(keyPath)
		if err != nil {
			log.Fatalf("failed to load public key: %v", err)
		}
		fmt.Print(string(ssh.MarshalAuthorizedKey(pubKey)))
		return
	case "chat":
		runChat(cmd.Args)
		return
	case "ui":
		if err := ui.Run(Version, ui.UIOptions{Sender: sender, Silent: silent}); err != nil {
			fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
			os.Exit(1)
		}
		return
	case "dashboard":
		req := &protocol.Request{Verb: "dashboard", Noun: "", Args: cmd.Args}
		resp, err := sender.Send(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
			os.Exit(1)
		}
		if resp.Status == protocol.StatusError {
			fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorRed, resp.Message, colorReset)
			os.Exit(1)
		}
		printResponse(resp.Data, cmd.OutputType)
		return
	case "run":
		runArgs := cmd.Args
		req := &protocol.Request{Verb: "run", Noun: cmd.Noun, Args: runArgs}
		resp, err := sender.Send(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
			os.Exit(1)
		}
		if resp.Status == protocol.StatusError {
			fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorRed, resp.Message, colorReset)
			os.Exit(1)
		}
		var result commands.CommandResult
		if json.Unmarshal(resp.Data, &result) == nil {
			fmt.Print(result.Output)
			if result.ExitCode != 0 {
				os.Exit(result.ExitCode)
			}
		} else {
			printResponse(resp.Data, cmd.OutputType)
		}
		return
	case "start":
		if cmd.Noun == "server" {
			runServer()
			return
		}
	}

	// Handle subcommand help client-side (no server needed).
	for _, a := range cmd.Args {
		if a == "-h" || a == "--help" {
			if help, ok := commands.CommandHelp[cmd.Verb+" "+cmd.Noun]; ok {
				fmt.Println(help)
			} else {
				fmt.Fprintf(os.Stderr, "No help available for: %s %s\n", cmd.Verb, cmd.Noun)
			}
			return
		}
	}

	// Everything else goes through the server.
	req := &protocol.Request{
		Verb: cmd.Verb,
		Noun: cmd.Noun,
		Args: cmd.Args,
	}

	resp, err := sender.Send(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	if resp.Status == protocol.StatusError {
		fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorRed, resp.Message, colorReset)
		os.Exit(1)
	}

	printResponse(resp.Data, cmd.OutputType)
}

// buildSender creates a Sender based on whether --hem was specified.
func buildSender(mi6Addr string) hemclient.Sender {
	if mi6Addr == "" {
		return &hemclient.SocketSender{SockPath: server.DefaultSocketPath()}
	}
	dataDir := defaultDataDir()
	keyPath := filepath.Join(dataDir, "hem_ecdsa")
	s := &hemclient.MI6Sender{Addr: mi6Addr, KeyPath: keyPath}
	if err := s.Connect(); err != nil {
		log.Fatalf("failed to connect to MI6 at %s: %v", mi6Addr, err)
	}
	return s
}

// verboseSender wraps a Sender and logs request/response details to stderr.
type verboseSender struct {
	inner hemclient.Sender
}

func (v *verboseSender) Send(req *protocol.Request) (*protocol.Response, error) {
	fmt.Fprintf(os.Stderr, "%s→ %s %s %v%s\n", colorMuted, req.Verb, req.Noun, req.Args, colorReset)
	start := time.Now()
	resp, err := v.inner.Send(req)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s✗ error after %v: %v%s\n", colorRed, elapsed, err, colorReset)
	} else {
		fmt.Fprintf(os.Stderr, "%s← %s (%v)%s\n", colorMuted, resp.Status, elapsed, colorReset)
	}
	return resp, err
}

// printResponse formats and prints the server response data.
func printResponse(data json.RawMessage, outputFmt string) {
	if len(data) == 0 {
		return
	}

	// Try to detect the data type and format accordingly.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Not an object, just print raw.
		fmt.Println(string(data))
		return
	}

	// Check if it's a TextResult (has "message" field).
	if msgField, ok := raw["message"]; ok {
		var msg string
		if json.Unmarshal(msgField, &msg) == nil {
			if outputFmt == output.FormatJSON {
				output.Print(os.Stdout, outputFmt, map[string]string{"message": msg})
			} else {
				fmt.Println(msg)
			}
			return
		}
	}

	// Check if it's a TableResult (has "headers" and "rows" fields).
	if _, hasHeaders := raw["headers"]; hasHeaders {
		var table commands.TableResult
		if json.Unmarshal(data, &table) == nil && table.Headers != nil {
			td := output.TableData{
				Headers: table.Headers,
				Rows:    table.Rows,
			}
			if td.Rows == nil {
				td.Rows = [][]string{}
			}
			// Default to table format for tabular data.
			tableFmt := outputFmt
			if tableFmt == output.FormatText {
				tableFmt = output.FormatTable
			}
			output.Print(os.Stdout, tableFmt, td)
			// Print warnings after the table.
			for _, w := range table.Warnings {
				fmt.Fprintf(os.Stderr, "%sWarning: %s%s\n", colorYellow, w, colorReset)
			}
			return
		}
	}

	// Check if it's a ProjectResult (has "default_agent" field).
	if _, hasDA := raw["default_agent"]; hasDA {
		var result commands.ProjectResult
		if json.Unmarshal(data, &result) == nil {
			td := output.TableData{
				Headers: []string{"Field", "Value"},
				Rows: [][]string{
					{"id", result.ID},
					{"name", result.Name},
					{"status", result.Status},
					{"moneypenny", result.Moneypenny},
					{"paths", result.Paths},
					{"default_agent", result.DefaultAgent},
					{"default_system_prompt", result.DefaultSystemPrompt},
				},
			}
			showFmt := outputFmt
			if showFmt == output.FormatText {
				showFmt = output.FormatTable
			}
			output.Print(os.Stdout, showFmt, td)
			return
		}
	}

	// Check if it's a SessionShowResult (has "moneypenny" field) — must be before
	// SessionStateResult since both have session_id + status.
	if _, hasMp := raw["moneypenny"]; hasMp {
		var result commands.SessionShowResult
		if json.Unmarshal(data, &result) == nil {
			td := output.TableData{
				Headers: []string{"Field", "Value"},
				Rows: [][]string{
					{"session_id", result.SessionID},
					{"moneypenny", result.Moneypenny},
					{"name", result.Name},
					{"agent", result.Agent},
					{"system_prompt", result.SystemPrompt},
					{"yolo", fmt.Sprintf("%v", result.Yolo)},
					{"path", result.Path},
					{"status", result.Status},
				},
			}
			showFmt := outputFmt
			if showFmt == output.FormatText {
				showFmt = output.FormatTable
			}
			output.Print(os.Stdout, showFmt, td)
			return
		}
	}

	// Check if it's a SessionCreatedResult or SessionContinuedResult.
	if _, hasSID := raw["session_id"]; hasSID {
		if _, hasResp := raw["response"]; hasResp {
			var result struct {
				SessionID string `json:"session_id"`
				Response  string `json:"response"`
				Async     bool   `json:"async"`
			}
			if json.Unmarshal(data, &result) == nil {
				if outputFmt == output.FormatJSON {
					output.Print(os.Stdout, outputFmt, result)
				} else {
					fmt.Printf("session_id: %s\n", result.SessionID)
					if result.Response != "" {
						fmt.Println(result.Response)
					}
				}
				return
			}
		}
		// SessionStateResult
		if _, hasStatus := raw["status"]; hasStatus {
			var result struct {
				SessionID string `json:"session_id"`
				Status    string `json:"status"`
			}
			if json.Unmarshal(data, &result) == nil {
				if outputFmt == output.FormatJSON {
					output.Print(os.Stdout, outputFmt, result)
				} else {
					fmt.Printf("Session %s: %s\n", result.SessionID, result.Status)
				}
				return
			}
		}
	}

	// Check if it's a HistoryResult (has "conversation" field).
	if _, hasConv := raw["conversation"]; hasConv {
		var result struct {
			SessionID    string `json:"session_id"`
			Conversation []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"conversation"`
		}
		if json.Unmarshal(data, &result) == nil {
			if outputFmt == output.FormatJSON {
				output.Print(os.Stdout, outputFmt, result.Conversation)
			} else {
				for _, turn := range result.Conversation {
					fmt.Printf("[%s]\n%s\n\n", turn.Role, turn.Content)
				}
			}
			return
		}
	}

	// Fallback: print as JSON.
	output.Print(os.Stdout, outputFmt, json.RawMessage(data))
}

// ANSI color codes for terminal output.
const (
	colorViolet    = "\033[35m"
	colorReset     = "\033[0m"
	colorYellow    = "\033[33m"
	colorRed       = "\033[31m"
	colorMuted     = "\033[90m"
)

// runChat runs an interactive chat session.
func runChat(args []string) {
	var mpName, sessionID, sessionName, systemPrompt, pathArg, agentName string
	var yolo bool

	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.StringVar(&mpName, "m", "", "moneypenny name")
	fs.StringVar(&mpName, "moneypenny", "", "moneypenny name")
	fs.StringVar(&sessionID, "session-id", "", "continue an existing session")
	fs.StringVar(&agentName, "agent", "", "agent to use")
	fs.StringVar(&sessionName, "name", "", "session name")
	fs.StringVar(&systemPrompt, "system-prompt", "", "system prompt")
	fs.BoolVar(&yolo, "yolo", false, "enable yolo mode")
	fs.StringVar(&pathArg, "path", "", "working directory path")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	sockPath := server.DefaultSocketPath()
	isNewSession := sessionID == ""

	// State for queuing messages while agent is working.
	var mu sync.Mutex
	var queued []string
	working := false

	// sendAndPrint sends a request to the server and prints the response.
	// Returns the session ID (important for first message).
	sendAndPrint := func(verb string, sendArgs []string) string {
		req := &protocol.Request{
			Verb: verb,
			Noun: "session",
			Args: sendArgs,
		}
		resp, err := hemclient.Send(sockPath, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sError: %v%s\n", colorRed, err, colorReset)
			return sessionID
		}
		if resp.Status == protocol.StatusError {
			fmt.Fprintf(os.Stderr, "%sError: %s%s\n", colorRed, resp.Message, colorReset)
			return sessionID
		}

		// Parse session response to get session_id and response text.
		var result struct {
			SessionID string `json:"session_id"`
			Response  string `json:"response"`
		}
		if err := json.Unmarshal(resp.Data, &result); err == nil {
			if result.SessionID != "" {
				sessionID = result.SessionID
			}
			if result.Response != "" {
				fmt.Printf("%s🤖 %s%s\n", colorViolet, result.Response, colorReset)
			}
		}
		return sessionID
	}

	// processMessage sends a message and then drains queued messages.
	processMessage := func(prompt string, isFirst bool) {
		if isFirst && isNewSession {
			// Build create session args.
			createArgs := []string{}
			if mpName != "" {
				createArgs = append(createArgs, "-m", mpName)
			}
			if agentName != "" {
				createArgs = append(createArgs, "--agent", agentName)
			}
			if sessionName != "" {
				createArgs = append(createArgs, "--name", sessionName)
			}
			if systemPrompt != "" {
				createArgs = append(createArgs, "--system-prompt", systemPrompt)
			}
			if yolo {
				createArgs = append(createArgs, "--yolo")
			}
			if pathArg != "" {
				createArgs = append(createArgs, "--path", pathArg)
			}
			createArgs = append(createArgs, prompt)
			sendAndPrint("create", createArgs)
		} else {
			continueArgs := []string{sessionID, prompt}
			sendAndPrint("continue", continueArgs)
		}

		// Drain any messages that were queued while we were waiting.
		for {
			mu.Lock()
			if len(queued) == 0 {
				working = false
				mu.Unlock()
				return
			}
			// Batch all queued messages into one prompt.
			batch := strings.Join(queued, "\n")
			queued = queued[:0]
			mu.Unlock()

			continueArgs := []string{sessionID, batch}
			sendAndPrint("continue", continueArgs)
		}
	}

	fmt.Fprintf(os.Stderr, "Chat started. Type messages and press Enter. Ctrl+C to exit.\n")

	scanner := bufio.NewScanner(os.Stdin)
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		mu.Lock()
		if working {
			// Agent is busy — queue this message.
			queued = append(queued, line)
			mu.Unlock()
			continue
		}
		working = true
		mu.Unlock()

		isFirst := first
		first = false
		processMessage(line, isFirst)
	}
}

// runServer starts the hem server daemon.
func runServer() {
	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	keyPath := filepath.Join(dataDir, "hem_ecdsa")

	dbPath := filepath.Join(dataDir, "hem.db")
	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to open store: %v", err)
	}
	defer st.Close()

	vlog := log.New(io.Discard, "[hem-server] ", log.LstdFlags)
	var mi6Control string
	// Parse flags from remaining args.
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-v":
			vlog = log.New(os.Stderr, "[hem-server] ", log.LstdFlags)
		case "--mi6-control":
			if i+1 < len(args) {
				i++
				mi6Control = args[i]
			}
		}
	}

	log.Printf("hem server v%s", Version)

	exec := commands.New(st, keyPath)
	exec.Version = Version

	// Check connectivity and sync sessions from moneypennies at startup.
	exec.CheckConnectivity(log.Default())
	go exec.SyncSessions(log.Default())

	// Sync sessions periodically (every 5 minutes).
	exec.StartPeriodicSync(log.Default(), 5*time.Minute)

	// Start MI6 control listener if configured.
	if mi6Control != "" {
		log.Printf("connecting MI6 control channel: %s", mi6Control)
		mi6Listener := server.NewMI6Listener(mi6Control, keyPath, exec, vlog)
		go mi6Listener.Run()
	}

	sockPath := server.DefaultSocketPath()

	srv := server.New(sockPath, exec, vlog)
	if err := srv.Run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: hem <verb> <noun> [flags]

Server:
  start server [-v]          Start the hem server daemon

Moneypenny management:
  add moneypenny -n NAME [--local | --fifo-folder DIR | --fifo-in/--fifo-out | --mi6 ADDR]
  list moneypennies
  ping moneypenny -n NAME
  delete moneypenny -n NAME
  set-default moneypenny -n NAME

Defaults:
  set-default agent VALUE        Set default agent (e.g. claude)
  set-default path VALUE         Set default working directory
  set-default mi6 HOST           Set default MI6 server address
  get-default agent|path|moneypenny|mi6
  list defaults

Project management:
  create project --name NAME [-m MP] [--path PATH] [--agent AGENT] [--system-prompt TEXT]
  list projects [--status active|paused|done]
  show project NAME_OR_ID
  update project NAME_OR_ID [--name, --status, -m, --path, --agent, --system-prompt]
  delete project NAME_OR_ID

Session management:
  create session [-m MONEYPENNY] [--project NAME] PROMPT [--name, --system-prompt, --yolo, --path, --async]
  continue session SESSION_ID PROMPT [--async]
  complete session SESSION_ID
  stop session SESSION_ID
  delete session SESSION_ID
  state session SESSION_ID
  last session SESSION_ID
  show session SESSION_ID
  update session SESSION_ID [--name, --system-prompt, --yolo true/false, --path]
  history session SESSION_ID [-n N]
  list sessions [-m MONEYPENNY] [--all] [--status STATUS]
  diff session SESSION_ID
  import session FILE.jsonl|SESSION_ID [-m MONEYPENNY] [--name, --project, --path]

Dashboard:
  dashboard [--project NAME] [--all]

Chat:
  chat [-m MONEYPENNY] [--session-id ID] [--agent, --name, --system-prompt, --yolo, --path]

Remote execution:
  run [-m MONEYPENNY] [--path PATH] [--session-id ID] COMMAND

MI6:
  test mi6 --mi6 ADDRESS --session SESSION_ID

UI:
  ui [--silent]          Open the interactive terminal UI

Other:
  show-public-key
  version

Global flags:
  --hem ADDR            Connect to hem server via MI6 instead of Unix socket
  -o, --output-type     Output format: json, text, table, tsv (default: text)`)
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/james/hem"
	}
	return filepath.Join(home, ".config", "james", "hem")
}

func loadOrCreatePublicKey(keyPath string) (ssh.PublicKey, error) {
	if _, err := os.Stat(keyPath); err == nil {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		rawKey, err := ssh.ParseRawPrivateKey(data)
		if err != nil {
			return nil, err
		}
		signer, ok := rawKey.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("expected ECDSA key, got %T", rawKey)
		}
		pub, err := ssh.NewPublicKey(&signer.PublicKey)
		return pub, err
	}
	return generateAndSaveKey(keyPath)
}

func generateAndSaveKey(keyPath string) (ssh.PublicKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	privBytes, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(privBytes), 0600); err != nil {
		return nil, err
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath+".pub", ssh.MarshalAuthorizedKey(pub), 0644); err != nil {
		return nil, err
	}
	log.Printf("generated new ECDSA key at %s", keyPath)
	return pub, nil
}
