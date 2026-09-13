package main

import (
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"time"

	"james/moneypenny/pkg/envelope"
)

const requestQueueSize = 64

type pendingRequest struct {
	ctx     context.Context
	cmd     *envelope.Command
	respond func(*envelope.Response)
	done    func()
	queued  time.Time
}

// Keep mutations ordered, but reserve independent workers for local snapshots.
// The dispatcher survives reconnects so a slow old request cannot overlap a
// newly received mutation.
// Diagnostic workers cannot bypass the shared transport writer: a stalled MI6
// write can still delay delivery of diagnostics. Explicit SQL scans use reads.
// Agent gadget admission still uses get_session for fresh permissions and can
// wait on reads; the operator's direct diagnostics request does not.
type requestDispatcher struct {
	commands          chan pendingRequest
	reads             chan pendingRequest
	diagnostics       chan pendingRequest
	commandActive     atomic.Int64
	readActive        atomic.Int64
	diagnosticsActive atomic.Int64
	telemetry         diagnosticTelemetry
	vlog              *log.Logger
}

func newRequestDispatcher(ctx context.Context, handle func(context.Context, *envelope.Command) *envelope.Response, vlog *log.Logger) *requestDispatcher {
	d := &requestDispatcher{
		commands:    make(chan pendingRequest, requestQueueSize),
		reads:       make(chan pendingRequest, requestQueueSize),
		diagnostics: make(chan pendingRequest, 4),
		vlog:        vlog,
	}
	go d.run(ctx, d.commands, &d.commandActive, handle)
	for i := 0; i < 2; i++ {
		go d.run(ctx, d.reads, &d.readActive, handle)
	}
	go d.run(ctx, d.diagnostics, &d.diagnosticsActive, handle)
	return d
}

func localSnapshot(method string) bool {
	switch method {
	case "list_sessions", "get_session", "get_session_conversation",
		"get_session_activity", "list_schedules", "list_channels",
		"get_version", "get_logs", "update_status":
		return true
	default:
		return false
	}
}

func (d *requestDispatcher) submit(ctx context.Context, cmd *envelope.Command, respond func(*envelope.Response), done func()) {
	ctx = d.telemetry.context(ctx, cmd.Method)
	queue := d.commands
	if localSnapshot(cmd.Method) {
		queue = d.reads
	}
	// The trigger only signals the serialized updater; it must not queue
	// behind the blocked command or session reads it is intended to recover.
	if cmd.Method == "force_update" {
		queue = d.diagnostics
	}
	if cmd.Method == "get_diagnostics" {
		queue = d.diagnostics
		var data envelope.GetDiagnosticsData
		if len(cmd.Data) <= 1024 && json.Unmarshal(cmd.Data, &data) == nil && data.ScanSession {
			queue = d.reads
		}
	}
	request := pendingRequest{ctx: ctx, cmd: cmd, respond: respond, done: done, queued: time.Now()}
	select {
	case <-ctx.Done():
		done()
	case queue <- request:
	default:
		d.vlog.Printf("busy: method=%s request_id=%s", cmd.Method, cmd.RequestID)
		respond(envelope.ErrorResponse(cmd.RequestID, envelope.ErrInternalError, "request queue is full; retry later"))
		done()
	}
}

func (d *requestDispatcher) snapshot() envelope.DispatcherDiagnostics {
	return envelope.DispatcherDiagnostics{
		Commands:    envelope.WorkerDiagnostics{Queued: len(d.commands), Capacity: cap(d.commands), Active: d.commandActive.Load(), Workers: 1},
		Reads:       envelope.WorkerDiagnostics{Queued: len(d.reads), Capacity: cap(d.reads), Active: d.readActive.Load(), Workers: 2},
		Diagnostics: envelope.WorkerDiagnostics{Queued: len(d.diagnostics), Capacity: cap(d.diagnostics), Active: d.diagnosticsActive.Load(), Workers: 1},
	}
}

func (d *requestDispatcher) run(ctx context.Context, queue <-chan pendingRequest, active *atomic.Int64, handle func(context.Context, *envelope.Command) *envelope.Response) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-queue:
			if ctx.Err() != nil {
				return
			}
			if request.ctx.Err() != nil {
				request.done()
				continue
			}
			start := time.Now()
			active.Add(1)
			traced := diagnosticTracing(request.ctx)
			if traced {
				d.vlog.Printf("exec: phase=handle_start method=%s request_id=%s queue_wait=%s", request.cmd.Method, request.cmd.RequestID, start.Sub(request.queued))
			}
			response := handle(request.ctx, request.cmd)
			if traced {
				d.vlog.Printf("handled: method=%s request_id=%s handle_duration=%s response_rows=%d", request.cmd.Method, request.cmd.RequestID, time.Since(start), diagnosticResponseRows(response))
			}
			if duration := time.Since(start); duration >= time.Second {
				d.vlog.Printf("slow request: method=%s request_id=%s duration=%s", request.cmd.Method, request.cmd.RequestID, duration)
			}
			if request.ctx.Err() == nil {
				request.respond(response)
			}
			active.Add(-1)
			request.done()
		}
	}
}
