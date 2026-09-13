//go:build !linux && !darwin && !windows

package handler

import (
	"context"

	"james/moneypenny/pkg/envelope"
)

func processDiagnostics(_ context.Context) envelope.ProcessDiagnostics {
	return envelope.ProcessDiagnostics{Error: "process memory unsupported on this operating system"}
}
