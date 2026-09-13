package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/handler"
	"james/moneypenny/pkg/store"
)

func TestDiagnosticsReservedWorkersAndAdmission(t *testing.T) {
	for _, secondRead := range []string{"get_session_conversation", "get_diagnostics"} {
		t.Run(secondRead, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan string, 3)
			replies := make(chan *envelope.Response, requestQueueSize+8)
			var d *requestDispatcher
			d = newRequestDispatcher(ctx, func(ctx context.Context, cmd *envelope.Command) *envelope.Response {
				if cmd.RequestID == "light" {
					return envelope.SuccessResponse(cmd.RequestID, d.snapshot())
				}
				started <- cmd.RequestID
				<-ctx.Done()
				return envelope.SuccessResponse(cmd.RequestID, nil)
			}, log.New(io.Discard, "", 0))
			submit := func(id, method, data string) {
				d.submit(ctx, &envelope.Command{RequestID: id, Method: method, Data: json.RawMessage(data)}, func(resp *envelope.Response) { replies <- resp }, func() {})
			}
			submit("mutation", "execute_command", `{}`)
			submit("read1", "get_session", `{}`)
			submit("read2", secondRead, `{"session_id":"target","scan_session":true}`)
			for range 3 {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("workers did not start")
				}
			}
			for range cap(d.reads) {
				submit("queued-scan", "get_diagnostics", `{"session_id":"target","scan_session":true}`)
			}
			submit("rejected-scan", "get_diagnostics", `{"session_id":"target","scan_session":true}`)
			submit("light", "get_diagnostics", `{}`)
			for range 2 {
				select {
				case resp := <-replies:
					switch resp.RequestID {
					case "rejected-scan":
						if resp.Status != envelope.StatusError {
							t.Fatal("full scan queue did not reject excess work")
						}
					case "light":
						state := resp.Data.(envelope.DispatcherDiagnostics)
						if state.Reads.Active != 2 || state.Commands.Active != 1 || state.Reads.Queued != requestQueueSize || state.Diagnostics.Active != 1 {
							t.Fatalf("unexpected dispatcher snapshot: %+v", state)
						}
					default:
						t.Fatalf("unexpected reply: %+v", resp)
					}
				case <-time.After(time.Second):
					t.Fatal("lightweight diagnostic blocked behind reads, mutations or scans")
				}
			}
		})
	}
}

func TestDiagnosticsRequestInstrumentation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	defer st.Close()
	h := handler.New(st, agent.New(log.New(io.Discard, "", 0)), "test", t.TempDir())
	for _, tc := range []struct {
		name   string
		data   interface{}
		rows   int
		status string
	}{
		{"rows", envelope.ListSchedulesResponse{Schedules: []envelope.ScheduleInfo{{Prompt: "secret-one"}, {Prompt: "secret-two"}}}, 2, envelope.StatusSuccess},
		{"encode-error", func() {}, -1, envelope.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs, output bytes.Buffer
			logger := log.New(&logs, "", 0)
			dispatcher := newRequestDispatcher(ctx, func(_ context.Context, cmd *envelope.Command) *envelope.Response {
				return envelope.SuccessResponse(cmd.RequestID, tc.data)
			}, logger)
			runStdio(ctx, h, dispatcher, logger, strings.NewReader(`{"type":"request","method":"list_schedules","request_id":"metrics","data":{}}`+"\n"), &output, true)
			var resp envelope.Response
			if err := json.Unmarshal(output.Bytes(), &resp); err != nil || resp.Status != tc.status {
				t.Fatalf("response: %+v error=%v", resp, err)
			}
			for _, field := range []string{
				"phase=handle_start", "phase=encode_start", "phase=write_start",
				"read_wait_duration=", "parse_duration=", "queue_wait=", "handle_duration=",
				"encode_duration=", "write_duration=", "process_heap_bytes=", "process_total_alloc_bytes=",
				fmt.Sprintf("status=%s bytes=%d response_rows=%d", tc.status, output.Len(), tc.rows),
			} {
				if !strings.Contains(logs.String(), field) {
					t.Errorf("missing %q in %s", field, logs.String())
				}
			}
			if strings.Contains(logs.String(), "secret-") {
				t.Fatal("instrumentation leaked payloads")
			}
		})
	}
}

func TestDiagnosticsTelemetryAdmission(t *testing.T) {
	var telemetry diagnosticTelemetry
	ctx := context.Background()
	budgets := []struct {
		method string
		limit  int
	}{
		{"get_session", diagnosticTracesPerSecond},
		{"list_schedules", diagnosticPayloadTracesPerSecond},
		{"get_session_conversation", diagnosticPayloadTracesPerSecond},
	}
	for _, budget := range budgets {
		for i := 0; i < budget.limit+1000; i++ {
			sampled := telemetry.context(ctx, budget.method)
			if diagnosticTracing(sampled) != (i < budget.limit) {
				t.Fatalf("unexpected %s sample admission %d", budget.method, i)
			}
			if telemetry.context(sampled, budget.method) != sampled {
				t.Fatal("sampling decision changed between lifecycle stages")
			}
		}
	}
	telemetry.window = time.Now().Add(-time.Second)
	for _, budget := range budgets {
		if !diagnosticTracing(telemetry.context(ctx, budget.method)) {
			t.Fatalf("%s sampling admission did not recover in next window", budget.method)
		}
	}
}

type diagnosticsCheckedPayload struct {
	check func()
}

func (p diagnosticsCheckedPayload) MarshalJSON() ([]byte, error) {
	p.check()
	return []byte(`{}`), nil
}

type diagnosticsCheckedWriter struct {
	check func()
	bytes.Buffer
}

func (w *diagnosticsCheckedWriter) Write(b []byte) (int, error) {
	w.check()
	return w.Buffer.Write(b)
}

func TestDiagnosticsPhaseStartsPrecedeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	h := handler.New(st, agent.New(logger), "test", t.TempDir())
	check := func(phase string) {
		if !strings.Contains(logs.String(), "phase="+phase) {
			t.Errorf("phase %s was not logged before work started", phase)
		}
	}
	d := newRequestDispatcher(ctx, func(_ context.Context, cmd *envelope.Command) *envelope.Response {
		check("handle_start")
		return envelope.SuccessResponse(cmd.RequestID, diagnosticsCheckedPayload{check: func() { check("encode_start") }})
	}, logger)
	output := &diagnosticsCheckedWriter{check: func() { check("write_start") }}
	runStdio(ctx, h, d, logger, strings.NewReader(`{"type":"request","method":"get_version","request_id":"phase","data":{}}`+"\n"), output, true)
}
