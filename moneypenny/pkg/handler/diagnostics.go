package handler

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"runtime/metrics"

	"james/moneypenny/pkg/envelope"
)

// SetDiagnostics configures startup-only metadata and a nonblocking dispatcher
// snapshot. The database path is the same path used to open the store.
func (h *Handler) SetDiagnostics(databasePath string, snapshot func() envelope.DispatcherDiagnostics) {
	h.diagnosticsDatabasePath = databasePath
	h.diagnosticsDispatcher = snapshot
}

func runtimeDiagnostics() envelope.RuntimeDiagnostics {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/gc/heap/objects:objects"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/sched/goroutines:goroutines"},
	}
	metrics.Read(samples)
	return envelope.RuntimeDiagnostics{
		HeapAllocBytes: samples[0].Value.Uint64(), HeapObjects: samples[1].Value.Uint64(),
		HeapReleasedBytes: samples[2].Value.Uint64(), TotalAllocBytes: samples[3].Value.Uint64(),
		GCCycles: samples[4].Value.Uint64(), Goroutines: samples[5].Value.Uint64(),
	}
}

func diagnosticFile(path string) envelope.DiagnosticsFile {
	if path == "" {
		return envelope.DiagnosticsFile{Error: "not configured"}
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return envelope.DiagnosticsFile{Error: "not present"}
		}
		return envelope.DiagnosticsFile{Error: "stat failed"}
	}
	size := info.Size()
	return envelope.DiagnosticsFile{Bytes: &size}
}

func (h *Handler) getDiagnostics(ctx context.Context, cmd *envelope.Command) *envelope.Response {
	var data envelope.GetDiagnosticsData
	if len(cmd.Data) > 1024 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "diagnostics data exceeds 1024 bytes")
	}
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &data); err != nil {
			return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "invalid diagnostics data")
		}
	}
	if data.ScanSession != (data.SessionID != "") || len(data.SessionID) > 256 {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInvalidRequest, "session scan requires scan_session=true and an exact session_id (at most 256 bytes)")
	}
	if ctx.Err() != nil {
		return envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "diagnostics request cancelled")
	}
	result := envelope.GetDiagnosticsResponse{
		PID: os.Getpid(), Runtime: runtimeDiagnostics(),
		Process: processDiagnostics(ctx), Database: h.store.DatabaseDiagnostics(),
		Files: envelope.DiagnosticsFiles{
			Database: diagnosticFile(h.diagnosticsDatabasePath),
			Log:      diagnosticFile(h.logFile),
		},
	}
	result.Process.OS = runtime.GOOS
	walPath := ""
	if h.diagnosticsDatabasePath != "" {
		walPath = h.diagnosticsDatabasePath + "-wal"
	}
	result.Files.WAL = diagnosticFile(walPath)
	if h.diagnosticsDispatcher != nil {
		snapshot := h.diagnosticsDispatcher()
		result.Dispatcher = &snapshot
	}
	if data.ScanSession {
		var err error
		result.Session, err = h.store.SessionDiagnostics(ctx, data.SessionID)
		if err != nil {
			result.SessionError = err.Error()
		}
	}
	return envelope.SuccessResponse(cmd.RequestID, result)
}
