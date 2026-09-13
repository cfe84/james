package handler

import (
	"context"
	"fmt"
	"os"

	"james/moneypenny/pkg/envelope"
)

func processDiagnostics(_ context.Context) envelope.ProcessDiagnostics {
	f, err := os.Open("/proc/self/statm")
	if err != nil {
		return envelope.ProcessDiagnostics{Error: "process memory unavailable: cannot open proc statm"}
	}
	defer f.Close()
	var virtual, resident uint64
	if _, err := fmt.Fscan(f, &virtual, &resident); err != nil {
		return envelope.ProcessDiagnostics{Error: "process memory unavailable: cannot parse proc statm"}
	}
	rss := resident * uint64(os.Getpagesize())
	return envelope.ProcessDiagnostics{RSSBytes: &rss}
}
