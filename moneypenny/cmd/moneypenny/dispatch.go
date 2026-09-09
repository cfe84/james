package main

import (
	"context"
	"log"
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
type requestDispatcher struct {
	commands chan pendingRequest
	reads    chan pendingRequest
	vlog     *log.Logger
}

func newRequestDispatcher(ctx context.Context, handle func(context.Context, *envelope.Command) *envelope.Response, vlog *log.Logger) *requestDispatcher {
	d := &requestDispatcher{
		commands: make(chan pendingRequest, requestQueueSize),
		reads:    make(chan pendingRequest, requestQueueSize),
		vlog:     vlog,
	}
	go d.run(ctx, d.commands, handle)
	for i := 0; i < 2; i++ {
		go d.run(ctx, d.reads, handle)
	}
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
	queue := d.commands
	if localSnapshot(cmd.Method) {
		queue = d.reads
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

func (d *requestDispatcher) run(ctx context.Context, queue <-chan pendingRequest, handle func(context.Context, *envelope.Command) *envelope.Response) {
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
			d.vlog.Printf("exec: method=%s request_id=%s queue_wait=%s", request.cmd.Method, request.cmd.RequestID, start.Sub(request.queued))
			response := handle(request.ctx, request.cmd)
			if duration := time.Since(start); duration >= time.Second {
				d.vlog.Printf("slow request: method=%s request_id=%s duration=%s", request.cmd.Method, request.cmd.RequestID, duration)
			}
			if request.ctx.Err() == nil {
				request.respond(response)
			}
			request.done()
		}
	}
}
