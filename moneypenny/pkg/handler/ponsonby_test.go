package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/memory"
)

func ponsonbyCall(t *testing.T, h *Handler, method string, p envelope.PonsonbyRequest) *envelope.Response {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return h.Handle(context.Background(), &envelope.Command{Method: method, RequestID: "ponsonby-test", Data: raw})
}

func TestPonsonbyCuratorIsolationAndDurableJobs(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	p := envelope.PonsonbyStore{ID: "00000000-0000-0000-0000-000000000081", Name: "engineering", Purpose: "Engineering decisions", Introduction: "No decisions yet.", Agent: "copilot", Model: "cheap-test-model"}
	if r := ponsonbyCall(t, h, "ponsonby_save", envelope.PonsonbyRequest{Store: p}); r.Status != envelope.StatusSuccess {
		t.Fatal(r)
	}
	job := envelope.PonsonbyJob{ID: "ingest-1", StoreID: p.ID, SourceID: gadgetSession, Kind: "ingest", Body: "Source turn 17: user selected Go."}
	if err := h.store.SubmitPonsonby(job); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SubmitPonsonby(job); err != nil {
		t.Fatal("idempotent submit failed:", err)
	}
	conflict := job
	conflict.Body = "different content"
	if err := h.store.SubmitPonsonby(conflict); err == nil {
		t.Fatal("request ID reused for different content")
	}
	var params agent.RunParams
	h.runAgentFunc = func(ctx context.Context, run agent.RunParams) (*agent.Result, error) {
		params = run
		if run.Yolo || !run.NoPersistTurns || !strings.Contains(run.Prompt, "Source turn 17") {
			t.Fatal("curator lost trusted execution policy or attribution")
		}
		if r := gadgetCall(t, run, "memory.set", map[string]string{"path": "engineering", "body": "Decisions"}); !r.Success {
			t.Fatal(r)
		}
		claim := memory.Claim{Path: "engineering", Title: "Language choice", Body: "User selected Go.", Kind: "decision", Status: "active", Confidence: "supported", Importance: "useful", Provenance: []memory.Provenance{{Method: "user", Source: "Session " + gadgetSession + ", turn 17"}}, Links: []memory.Link{}}
		if r := gadgetCall(t, run, "memory.claim-set", claim); !r.Success {
			t.Fatal(r)
		}
		requireGadgetError(t, gadgetCall(t, run, "agents.list", map[string]any{}), "permission_denied")
		return &agent.Result{Text: "Published language decision."}, nil
	}
	active, err := h.store.NextPonsonbyJob()
	if err != nil || active == nil {
		t.Fatal(active, err)
	}
	// Simulate a restart after acquisition: interrupted work must be runnable again.
	if err := h.store.RecoverPonsonbyJobs(); err != nil {
		t.Fatal(err)
	}
	active, err = h.store.NextPonsonbyJob()
	if err != nil || active == nil {
		t.Fatal(active, err)
	}
	answer, err := h.runPonsonby(context.Background(), *active)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.FinishPonsonbyJob(active.ID, answer, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PonsonbyJob(job.ID, gadgetOtherSession); err != nil {
		t.Fatal(err)
	}
	wrong, _ := h.store.PonsonbyJob(job.ID, gadgetOtherSession)
	if wrong != nil {
		t.Fatal("another session retrieved the job")
	}
	if session, _ := h.store.GetSession(params.SessionID); session != nil {
		t.Fatal("temporary curator session leaked")
	}
	requireGadgetError(t, gadgetCall(t, params, "memory.get", map[string]any{}), "unauthorized")
	own, err := memory.QueryClaims(h.memoryDir(gadgetSession), memory.Query{})
	if err != nil || len(own.Items) != 0 {
		t.Fatal("shared claims leaked into local session memory")
	}
	h.runAgentFunc = func(ctx context.Context, run agent.RunParams) (*agent.Result, error) {
		r := gadgetCall(t, run, "memory.query", map[string]string{"path": "engineering"})
		raw, _ := json.Marshal(r.Data)
		if !r.Success || !strings.Contains(string(raw), "Language choice") {
			t.Fatal("consultation cannot see published shared claim")
		}
		for _, method := range []string{"memory.set", "memory.claim-set", "memory.delete"} {
			requireGadgetError(t, gadgetCall(t, run, method, map[string]any{}), "permission_denied")
		}
		return &agent.Result{Text: "User selected Go (session/turn evidence; supported)."}, nil
	}
	ask := envelope.PonsonbyJob{ID: "ask-1", StoreID: p.ID, SourceID: gadgetOtherSession, Kind: "ask", Body: "Which language?"}
	if _, err := h.runPonsonby(context.Background(), ask); err != nil {
		t.Fatal(err)
	}
	h.runAgentFunc = func(ctx context.Context, run agent.RunParams) (*agent.Result, error) {
		return nil, errors.New("model unavailable")
	}
	if _, err := h.runPonsonby(context.Background(), ask); err == nil {
		t.Fatal("curator failure hidden")
	}
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"maximum", strings.Repeat("a", 8000), true},
		{"too-long", strings.Repeat("a", 8001), false},
		{"encoded-overflow", strings.Repeat(`"`, 8000), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
				return &agent.Result{Text: tc.text}, nil
			}
			answer, err := h.runPonsonby(context.Background(), ask)
			if (err == nil) != tc.valid || (tc.valid && answer != tc.text) {
				t.Fatal("wrong answer budget", len(answer), err)
			}
		})
	}
	if err := h.store.SubmitPonsonby(ask); err != nil {
		t.Fatal(err)
	}
	if err := h.store.FinishPonsonbyJob(ask.ID, "", "model unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.RetryPonsonby(ask.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	retry, _ := h.store.NextPonsonbyJob()
	if retry == nil || retry.Body != ask.Body {
		t.Fatal("failed-job retry lost original question")
	}
	if err := h.store.FinishPonsonbyJob(ask.ID, "answer", ""); err != nil {
		t.Fatal(err)
	}
	if r := ponsonbyCall(t, h, "ponsonby_delete", envelope.PonsonbyRequest{Store: p}); r.Status != envelope.StatusSuccess {
		t.Fatal(r)
	}
	if shared, _ := h.store.GetPonsonby(p.ID); shared != nil {
		t.Fatal("deleted store still registered")
	}
}

func TestPonsonbyAssignmentsIndependentOfLocalMemory(t *testing.T) {
	h, params := gadgetTestHandler(t)
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	a := envelope.Ponsonby{ID: "00000000-0000-0000-0000-000000000081", Name: "engineering", Purpose: "Decisions", Introduction: "Go decisions", Publish: false}
	if err := h.store.SetPonsonbySources(gadgetSession, []envelope.Ponsonby{a}); err != nil {
		t.Fatal(err)
	}
	params.SystemPrompt = ""
	if err := h.prepareRunInstructions(gadgetSession, &params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params.SystemPrompt, "gadgets ponsonby ask") || !strings.Contains(params.SystemPrompt, "read-only") {
		t.Fatal("shared memory unavailable when local memory disabled")
	}
	if err := h.store.SetPonsonbySources(gadgetSession, []envelope.Ponsonby{}); err != nil {
		t.Fatal(err)
	}
	requireGadgetError(t, gadgetCall(t, params, "ponsonby.ask", map[string]any{"store": a.ID, "body": "question"}), "permission_denied")
	requireGadgetError(t, gadgetCall(t, params, "ponsonby.ingest", map[string]any{}), "unknown_method")
}

func TestPonsonbyQueueCapacityAndCancellation(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	p := envelope.PonsonbyStore{ID: "00000000-0000-0000-0000-000000000081", Name: "engineering", Purpose: "Engineering", Agent: "copilot", Model: "test"}
	if err := h.store.SavePonsonby(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		j := envelope.PonsonbyJob{ID: fmt.Sprintf("request-%02d", i), StoreID: p.ID, SourceID: gadgetSession, Kind: "ask", Body: "Question"}
		if err := h.store.SubmitPonsonby(j); err != nil {
			t.Fatal(err)
		}
	}
	extra := envelope.PonsonbyJob{ID: "extra", StoreID: p.ID, SourceID: gadgetSession, Kind: "ask", Body: "Question"}
	if err := h.store.SubmitPonsonby(extra); err == nil {
		t.Fatal("queue capacity not enforced")
	}
	if err := h.store.FinishPonsonbyJob("request-00", "done", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SubmitPonsonby(extra); err != nil {
		t.Fatal("completed work blocked new request", err)
	}
	h.runAgentFunc = func(ctx context.Context, p agent.RunParams) (*agent.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.runPonsonby(ctx, extra); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	if runs, err := h.store.PonsonbyRuns(); err != nil || len(runs) != 0 {
		t.Fatal("cancelled run leaked resources", runs, err)
	}
	for i := 1; i < 64; i++ {
		if err := h.store.FinishPonsonbyJob(fmt.Sprintf("request-%02d", i), "done", ""); err != nil {
			t.Fatal(err)
		}
	}
	h.runAgentFunc = func(context.Context, agent.RunParams) (*agent.Result, error) {
		return &agent.Result{Text: "Qualified answer"}, nil
	}
	if err := h.StartPonsonby(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	// The worker ticks once per second; allow scheduling slack under -race.
	timeout := time.After(5 * time.Second)
	for {
		select {
		case <-tick.C:
			result, err := h.store.PonsonbyJob(extra.ID, extra.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			if result != nil && result.Status == "done" {
				if result.Answer != "Qualified answer" {
					t.Fatal("worker lost result", result)
				}
				return
			}
		case <-timeout:
			t.Fatal("worker did not persist queued result")
		}
	}
}

func TestPonsonbyTranscriptDeliveryRetriesAndSkipsReadOnly(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	socket := fmt.Sprintf(".ponsonby-%d.sock", time.Now().UnixNano())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}

	defer listener.Close()
	calls := make(chan gadgetHemRequest, 8)
	failures := 1
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request gadgetHemRequest
			err = json.NewDecoder(conn).Decode(&request)
			if err == nil {
				calls <- request
				response := gadgetHemResponse{Status: "ok", RequestID: request.RequestID, Data: json.RawMessage(`{"accepted":true}`)}
				if failures > 0 {
					failures--
					response.Status = "error"
					response.Message = "remote unavailable"
				}
				_ = json.NewEncoder(conn).Encode(response)
			}
			conn.Close()
		}
	}()
	defer func() { listener.Close(); <-done }()
	raw, _ := json.Marshal(map[string]any{"session_id": gadgetSession, "gadget_route": map[string]string{"JAMES_HEM_SOCKET": socket}})
	if r := h.Handle(context.Background(), &envelope.Command{Method: "get_session", Data: raw}); r.Status != envelope.StatusSuccess {
		t.Fatal(r)
	}
	a := envelope.Ponsonby{ID: "00000000-0000-0000-0000-000000000081", Publish: true}
	if err := h.store.SetPonsonbySources(gadgetSession, []envelope.Ponsonby{a}); err != nil {
		t.Fatal(err)
	}
	for _, turn := range []struct{ role, body string }{{"user", strings.Repeat("é", 13000)}, {"assistant", "Decision recorded"}, {"thinking", "Private intermediate reasoning"}} {
		if err := h.store.AddConversationTurn(gadgetSession, turn.role, turn.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.publishPonsonbyTurns(context.Background()); err == nil {
		t.Fatal("delivery failure hidden")
	}
	if err := h.publishPonsonbyTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, second := <-calls, <-calls
	if first.Args[0] != second.Args[0] {
		t.Fatal("retry changed delivery identity/content")
	}
	for i := 0; i < 2; i++ {
		request := <-calls
		if !strings.Contains(request.Args[0], "ponsonby.ingest") || strings.Contains(request.Args[0], "Private intermediate reasoning") {
			t.Fatal("wrong transcript sent")
		}
	}
	if turns, err := h.store.UnpublishedPonsonbyTurns(); err != nil || len(turns) != 0 {
		t.Fatal("successful publication did not advance cursor", turns, err)
	}
	a.Publish = false
	if err := h.store.SetPonsonbySources(gadgetSession, []envelope.Ponsonby{a}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddConversationTurn(gadgetSession, "user", "Do not share this"); err != nil {
		t.Fatal(err)
	}
	if err := h.publishPonsonbyTurns(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-calls:
		t.Fatal("read-only assignment published:", r)
	default:
	}
	a.Publish = true
	if err := h.store.SetPonsonbySources(gadgetSession, []envelope.Ponsonby{a}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddConversationTurn(gadgetSession, "user", string([]byte{0x80})); err != nil {
		t.Fatal(err)
	}
	if err := h.publishPonsonbyTurns(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatal("malformed transcript was not rejected", err)
	}
}
