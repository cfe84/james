package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
)

func TestCommandJobHelperProcess(t *testing.T) {
	if os.Getenv("JAMES_TEST_JOB_CHILD") == "" {
		return
	}
	if os.Getenv("JAMES_TEST_JOB_CHILD") == "wait" {
		time.Sleep(time.Minute)
		return
	}
	fmt.Fprint(os.Stdout, "job stdout")
	fmt.Fprint(os.Stderr, "job stderr")
	os.Exit(7)
}

func TestGadgetCommandJobLifecycle(t *testing.T) {
	h, params := gadgetTestHandler(t)
	requireGadgetError(t, gadgetCall(t, params, "run.start", map[string]any{"argv": []string{"echo", "hello"}}), "permission_denied")
	yolo := true
	payload, _ := json.Marshal(envelope.UpdateSessionData{SessionID: gadgetSession, Yolo: &yolo})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: payload}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("grant License to Kill: %+v", resp)
	}

	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		return &agent.Result{Text: "acknowledged"}, nil
	}
	t.Setenv("JAMES_TEST_JOB_CHILD", "output")
	response := gadgetCall(t, params, "run.start", map[string]any{"argv": []string{os.Args[0], "-test.run=TestCommandJobHelperProcess"}})
	if !response.Success {
		t.Fatalf("start job: %+v", response)
	}
	raw, _ := json.Marshal(response.Data)
	var job commandJob
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Stdout == "" || job.Stderr == "" {
		t.Fatalf("incomplete job response: %+v", job)
	}
	if _, err := os.Stat(job.Stdout); err != nil {
		t.Fatalf("stdout not readable while running: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := h.readCommandJob(gadgetSession, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status != "running" {
			if status.Status != "failed" || status.ExitCode == nil || *status.ExitCode != 7 {
				t.Fatalf("unexpected job status: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, tc := range []struct{ path, want string }{{job.Stdout, "job stdout"}, {job.Stderr, "job stderr"}} {
		body, err := os.ReadFile(tc.path)
		if err != nil || !strings.Contains(string(body), tc.want) {
			t.Fatalf("output %s: %q, %v", tc.path, body, err)
		}
	}
	for {
		turns, err := h.store.GetConversationPaginated(gadgetSession, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, turn := range turns {
			if turn.Role == "callback" && strings.Contains(turn.Content, "exit_code=7") && strings.Contains(turn.Content, job.ID) {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completion callback not delivered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireGadgetError(t, gadgetCall(t, params, "run.status", map[string]string{"id": strings.Repeat("f", 32)}), "not_found")
	t.Setenv("JAMES_TEST_JOB_CHILD", "wait")
	waiting := gadgetCall(t, params, "run.start", map[string]any{"argv": []string{os.Args[0], "-test.run=TestCommandJobHelperProcess"}})
	if !waiting.Success {
		t.Fatalf("start waiting job: %+v", waiting)
	}
	raw, _ = json.Marshal(waiting.Data)
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if stopped := gadgetCall(t, params, "run.stop", map[string]string{"id": job.ID}); !stopped.Success {
		t.Fatalf("stop job: %+v", stopped)
	}
	h.stopSessionCommandJobs(gadgetSession)
	status, err := h.readCommandJob(gadgetSession, job.ID)
	if err != nil || status.Status != "stopped" {
		t.Fatalf("stopped status: %+v, %v", status, err)
	}
	yolo = false
	payload, _ = json.Marshal(envelope.UpdateSessionData{SessionID: gadgetSession, Yolo: &yolo})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "update_session", Data: payload}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("revoke License to Kill: %+v", resp)
	}
	requireGadgetError(t, gadgetCall(t, params, "run.list", map[string]any{}), "permission_denied")
}

func TestCommandJobRestartMarksInterruptedAndQueuesCallback(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	h.runAgentFunc = func(_ context.Context, _ agent.RunParams) (*agent.Result, error) {
		return &agent.Result{Text: "acknowledged"}, nil
	}
	id := strings.Repeat("a", 32)
	dir, err := h.jobPath(gadgetSession, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	job := &commandJob{ID: id, Status: "running", Stdout: dir + "/stdout", Stderr: dir + "/stderr"}
	if err := saveCommandJob(job, dir); err != nil {
		t.Fatal(err)
	}
	h.recoverCommandJobs()
	recovered, err := h.readCommandJob(gadgetSession, id)
	if err != nil || recovered.Status != "interrupted" || recovered.ExitCode != nil {
		t.Fatalf("recovered job: %+v, %v", recovered, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		turns, err := h.store.GetConversationPaginated(gadgetSession, 20, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range turns {
			if turn.Role == "callback" && strings.Contains(turn.Content, "status=interrupted") {
				h.recoverCommandJobs()
				replayed, err := h.store.GetConversationPaginated(gadgetSession, 20, 0)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, saved := range replayed {
					if saved.Role == "callback" && strings.Contains(saved.Content, id) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("restart replayed callback %d times", count)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("restart interruption callback missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBoundedCommandOutput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := &boundedOutput{file: file, remaining: 4}
	if n, err := writer.Write([]byte("12345678")); n != 8 || err != nil || !writer.truncated {
		t.Fatalf("bounded write: n=%d err=%v truncated=%v", n, err, writer.truncated)
	}
	body, err := os.ReadFile(file.Name())
	if err != nil || string(body) != "1234" {
		t.Fatalf("bounded contents = %q: %v", body, err)
	}
}

func TestExpiredCommandJobRemovesOutput(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	id := strings.Repeat("b", 32)
	dir, err := h.jobPath(gadgetSession, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	job := &commandJob{ID: id, Status: "completed", EndedAt: time.Now().Add(-commandRetention - time.Hour),
		Stdout: dir + "/stdout", Stderr: dir + "/stderr"}
	if err := saveCommandJob(job, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Stdout, []byte("old output"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := h.commandJobEntries(gadgetSession)
	if err != nil || len(entries) != 0 {
		t.Fatalf("prune expired job: %+v, %v", entries, err)
	}
	if _, err := os.Stat(job.Stdout); !os.IsNotExist(err) {
		t.Fatalf("expired output remains: %v", err)
	}
}
