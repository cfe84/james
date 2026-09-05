package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// RunAgencyPlugin serves the existing Teams Agency provider over JSONL stdio.
// It is used by the james-teams-agency executable and deliberately keeps all
// provider credentials and MCP details inside the child process.
func RunAgencyPlugin(ctx context.Context, in io.Reader, out io.Writer, command string) error {
	provider := newTeamsProvider(newMCPClient(command, "mcp", "teams"))
	defer provider.Close()
	enc := json.NewEncoder(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req pluginMessage
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.Type != "request" {
			continue
		}
		result, err := runPluginMethod(ctx, provider, req.Method, req.Params)
		resp := pluginMessage{Type: "response", ID: req.ID}
		if req.Version != "" && req.Version != ChannelPluginProtocolVersion {
			err = fmt.Errorf("unsupported channel plugin protocol version %q", req.Version)
		}
		if err != nil {
			resp.Error = &pluginError{Code: "provider_error", Message: err.Error()}
		} else {
			resp.Result = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func runPluginMethod(ctx context.Context, p Provider, method string, raw json.RawMessage) (interface{}, error) {
	var args map[string]string
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("decode %s parameters: %w", method, err)
		}
	}
	switch method {
	case "initialize":
		return map[string]interface{}{"protocol_version": ChannelPluginProtocolVersion, "name": p.Name(), "caps": p.Caps()}, nil
	case "search":
		return p.Search(ctx, args["query"])
	case "resolve":
		return p.Resolve(ctx, args["target_id"])
	case "list_messages":
		return p.ListMessages(ctx, args["target_id"], args["since_ts"])
	case "send":
		id, err := p.Send(ctx, args["target_id"], args["sender_name"], args["content"])
		return map[string]string{"id": id}, err
	case "latest_cursor":
		id, ts, err := p.LatestCursor(ctx, args["target_id"])
		return map[string]string{"id": id, "timestamp": ts}, err
	case "self":
		id, name, err := p.Self(ctx)
		return map[string]string{"id": id, "name": name}, err
	default:
		return nil, fmt.Errorf("unknown plugin method %q", method)
	}
}

// RunAgencyPluginFromEnv is convenient for the standalone executable.
func RunAgencyPluginFromEnv(ctx context.Context) error {
	command := os.Getenv("AGENCY_COMMAND")
	if command == "" {
		command = "agency"
	}
	return RunAgencyPlugin(ctx, os.Stdin, os.Stdout, command)
}
