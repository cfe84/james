package commands

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/envelope"
)

// MoneypennyDiagnosticsResult identifies the host supplying a bounded diagnostic sample.
type MoneypennyDiagnosticsResult struct {
	Moneypenny  string                          `json:"moneypenny"`
	Diagnostics envelope.GetDiagnosticsResponse `json:"diagnostics"`
}

// diagnoseMoneypenny samples one daemon without pinging or enumerating other hosts.
func (e *Executor) diagnoseMoneypenny(args []string) *protocol.Response {
	var name, sessionID string
	var scan bool
	remaining, err := parseFlagsFromArgs("diagnose", args, func(fs *flag.FlagSet) {
		fs.StringVar(&name, "name", "", "registered moneypenny name (required)")
		fs.StringVar(&name, "n", "", "registered moneypenny name (required)")
		fs.StringVar(&sessionID, "session-id", "", "exact session ID for database diagnostics (requires --scan)")
		fs.BoolVar(&scan, "scan", false, "opt in to database row/byte aggregates for the selected session")
	})
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if len(remaining) != 0 || name == "" || scan != (sessionID != "") {
		return protocol.ErrResponse("usage: diagnose --name HOST [--session-id ID --scan]")
	}
	mp, err := e.store.GetMoneypenny(name)
	if err != nil {
		return protocol.ErrResponse(err.Error())
	}
	if mp == nil {
		return protocol.ErrResponse(fmt.Sprintf("moneypenny %q not found", name))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := e.sendCommand(ctx, mp, "get_diagnostics", envelope.GetDiagnosticsData{SessionID: sessionID, ScanSession: scan})
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("get_diagnostics failed (target daemon must support diagnostics): %v", err))
	}
	var result envelope.GetDiagnosticsResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil || result.PID <= 0 || result.Runtime.Goroutines == 0 || result.Process.OS == "" || result.Dispatcher == nil {
		return protocol.ErrResponse("invalid moneypenny diagnostics response")
	}
	if scan && result.SessionError == "" && (result.Session == nil || result.Session.SessionID != sessionID) {
		return protocol.ErrResponse("moneypenny diagnostics omitted the requested session scan")
	}
	return protocol.OKResponse(MoneypennyDiagnosticsResult{Moneypenny: name, Diagnostics: result})
}
