package handler

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"james/moneypenny/pkg/envelope"
)

func processDiagnostics(ctx context.Context) envelope.ProcessDiagnostics {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	// ps selects this PID only and emits one numeric field, never command lines.
	output, err := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return envelope.ProcessDiagnostics{Error: "process RSS unavailable: ps failed or timed out"}
	}
	kb, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return envelope.ProcessDiagnostics{Error: "process RSS unavailable: invalid ps result"}
	}
	rss := kb * 1024
	return envelope.ProcessDiagnostics{RSSBytes: &rss}
}
