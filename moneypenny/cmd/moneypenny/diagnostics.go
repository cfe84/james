package main

import (
	"context"
	"runtime/metrics"
	"sync"
	"time"

	"james/moneypenny/pkg/envelope"
)

const (
	diagnosticTracesPerSecond        = 10
	diagnosticPayloadTracesPerSecond = 2
)

type diagnosticTraceKey struct{}

// Sampling admission is process-wide (the dispatcher survives reconnects).
// A sampled request retains every phase, including starts before expensive work.
// Payload reads have reserved budgets so frequent metadata polls cannot hide them.
type diagnosticTelemetry struct {
	mu     sync.Mutex
	window time.Time
	counts [3]int
}

func (d *diagnosticTelemetry) context(ctx context.Context, method string) context.Context {
	if _, ok := ctx.Value(diagnosticTraceKey{}).(bool); ok {
		return ctx
	}
	budget, limit := 0, diagnosticTracesPerSecond
	switch method {
	case "list_schedules":
		budget, limit = 1, diagnosticPayloadTracesPerSecond
	case "get_session_conversation":
		budget, limit = 2, diagnosticPayloadTracesPerSecond
	}
	d.mu.Lock()
	now := time.Now()
	if now.Sub(d.window) >= time.Second {
		d.window, d.counts = now, [3]int{}
	}
	traced := d.counts[budget] < limit
	if traced {
		d.counts[budget]++
	}
	d.mu.Unlock()
	return context.WithValue(ctx, diagnosticTraceKey{}, traced)
}

func diagnosticTracing(ctx context.Context) bool {
	traced, _ := ctx.Value(diagnosticTraceKey{}).(bool)
	return traced
}

// Unknown row counts use -1; this never walks or serializes response contents.
func diagnosticResponseRows(response *envelope.Response) int {
	if response == nil || response.Status != envelope.StatusSuccess {
		return -1
	}
	switch data := response.Data.(type) {
	case envelope.SessionDetail:
		return 1
	case []envelope.SessionInfo:
		return len(data)
	case envelope.SessionConversation:
		return len(data.Conversation)
	case envelope.ListSchedulesResponse:
		return len(data.Schedules)
	case envelope.ListChannelsResponse:
		return len(data.Channels)
	case envelope.SessionActivityResponse:
		return len(data.Activity)
	case envelope.ListMemoryResponse:
		return len(data.Children)
	case envelope.SearchMemoryResponse:
		return len(data.Results)
	default:
		return -1
	}
}

func diagnosticHeapCounters() (heap, total uint64) {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/gc/heap/allocs:bytes"},
	}
	metrics.Read(samples)
	return samples[0].Value.Uint64(), samples[1].Value.Uint64()
}
