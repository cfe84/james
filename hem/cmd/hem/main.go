package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"james/hem/pkg/cli"
	"james/hem/pkg/commands"
	"james/hem/pkg/hemclient"
	"james/hem/pkg/output"
	"james/hem/pkg/protocol"
	"james/hem/pkg/server"
	"james/hem/pkg/store"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd, err := cli.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
	case "server":
		runServer()
		return
	}

	// Everything else goes through the server.
	sockPath := server.DefaultSocketPath()
	req := &protocol.Request{
		Verb: cmd.Verb,
		Noun: cmd.Noun,
		Args: cmd.Args,
	}

	resp, err := hemclient.Send(sockPath, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if resp.Status == protocol.StatusError {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Message)
		os.Exit(1)
	}

	// Format and print the response data.
	printResponse(resp.Data, cmd.OutputType)
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
			fmt := outputFmt
			if fmt == output.FormatText {
				fmt = output.FormatTable
			}
			output.Print(os.Stdout, fmt, td)
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
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"conversation"`
		}
		if json.Unmarshal(data, &result) == nil {
			if outputFmt == output.FormatJSON {
				output.Print(os.Stdout, outputFmt, result.Conversation)
			} else {
				for _, turn := range result.Conversation {
					fmt.Printf("[%s]\n%s\n\n", turn.Role, turn.Text)
				}
			}
			return
		}
	}

	// Check if it's a SessionShowResult (has "moneypenny" field).
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

	// Fallback: print as JSON.
	output.Print(os.Stdout, outputFmt, json.RawMessage(data))
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
	// Check for -v flag in remaining args.
	for _, arg := range os.Args[2:] {
		if arg == "-v" {
			vlog = log.New(os.Stderr, "[hem-server] ", log.LstdFlags)
			break
		}
	}

	exec := commands.New(st, keyPath)
	sockPath := server.DefaultSocketPath()

	srv := server.New(sockPath, exec, vlog)
	if err := srv.Run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: hem <verb> <noun> [flags]

Server:
  server [-v]                Start the hem server daemon

Moneypenny management:
  add moneypenny -n NAME [--fifo-folder DIR | --fifo-in/--fifo-out | --mi6 ADDR]
  list moneypennies
  ping moneypenny -n NAME
  delete moneypenny -n NAME
  set-default moneypenny -n NAME

Session management:
  create session -m MONEYPENNY PROMPT [--name, --system-prompt, --yolo, --path, --async]
  continue session SESSION_ID PROMPT [--async]
  stop session SESSION_ID
  delete session SESSION_ID
  state session SESSION_ID
  last session SESSION_ID
  show session SESSION_ID
  history session SESSION_ID [-n N]
  list sessions [-m MONEYPENNY]

Other:
  show-public-key
  version

Global flags:
  -o, --output-type   Output format: json, text, table, tsv (default: text)`)
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
