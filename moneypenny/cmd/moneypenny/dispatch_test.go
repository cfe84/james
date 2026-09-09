package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/handler"
	"james/moneypenny/pkg/store"
)

func TestStdioListsSessionsWhileCommandBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := log.New(io.Discard, "", 0)
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateSession(&store.Session{SessionID: "session", Name: "Busy agent", Agent: "copilot"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSessionStatus("session", store.StateWorking); err != nil {
		t.Fatal(err)
	}
	h := handler.New(st, agent.New(logger), "test", t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	dispatcher := newRequestDispatcher(ctx, func(ctx context.Context, cmd *envelope.Command) *envelope.Response {
		if cmd.Method == "execute_command" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return envelope.SuccessResponse(cmd.RequestID, nil)
		}
		return h.Handle(ctx, cmd)
	}, logger)
	input, send := io.Pipe()
	output, receive := io.Pipe()
	defer input.Close()
	defer send.Close()
	defer output.Close()
	defer receive.Close()
	stopped := make(chan struct{})
	go func() {
		runStdio(ctx, h, dispatcher, logger, input, receive, false)
		close(stopped)
	}()
	if _, err := fmt.Fprintln(send, `{"type":"request","method":"execute_command","request_id":"slow","data":{}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow command did not start")
	}
	for _, method := range []string{"list_sessions", "get_version"} {
		if _, err := fmt.Fprintf(send, "{\"type\":\"request\",\"method\":%q,\"request_id\":%q,\"data\":{}}\n", method, method); err != nil {
			t.Fatal(err)
		}
	}
	replies := make(chan envelope.Response, 2)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var resp envelope.Response
			if json.Unmarshal(scanner.Bytes(), &resp) == nil {
				replies <- resp
			}
		}
	}()
	seen := map[string]bool{}
	for range 2 {
		select {
		case resp := <-replies:
			if resp.Status != envelope.StatusSuccess {
				t.Fatalf("snapshot failed: %+v", resp)
			}
			if resp.RequestID == "list_sessions" {
				data, err := json.Marshal(resp.Data)
				if err != nil {
					t.Fatal(err)
				}
				var sessions []envelope.SessionInfo
				if err := json.Unmarshal(data, &sessions); err != nil {
					t.Fatal(err)
				}
				if len(sessions) != 1 || sessions[0].Name != "Busy agent" || sessions[0].Status != store.StateWorking || sessions[0].Agent != "copilot" {
					t.Fatalf("snapshot lost live metadata: %+v", sessions)
				}
			}
			seen[resp.RequestID] = true
		case <-time.After(time.Second):
			t.Fatal("snapshot waited for the slow command")
		}
	}
	if !seen["list_sessions"] || !seen["get_version"] {
		t.Fatalf("missing snapshots: %v", seen)
	}
	send.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("disconnected stream waited for a command")
	}
}

func TestDispatcherBoundsMutationQueueWithoutBlockingSnapshots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	mutations := make(chan string, requestQueueSize+1)
	d := newRequestDispatcher(ctx, func(ctx context.Context, cmd *envelope.Command) *envelope.Response {
		if cmd.Method == "execute_command" {
			mutations <- cmd.RequestID
			if cmd.RequestID == "first" {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		}
		return envelope.SuccessResponse(cmd.RequestID, nil)
	}, log.New(io.Discard, "", 0))
	responses := make(chan *envelope.Response, requestQueueSize+3)
	submit := func(method, id string) {
		d.submit(ctx, &envelope.Command{Method: method, RequestID: id}, func(resp *envelope.Response) { responses <- resp }, func() {})
	}
	submit("execute_command", "first")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first command did not start")
	}
	for i := 0; i < requestQueueSize; i++ {
		submit("execute_command", fmt.Sprint(i))
	}
	submit("execute_command", "overflow")
	submit("list_sessions", "snapshot")
	for range 2 {
		select {
		case resp := <-responses:
			if resp.RequestID == "overflow" {
				if resp.Status != envelope.StatusError {
					t.Fatalf("queue overflow must be explicit: %+v", resp)
				}
			} else if resp.RequestID != "snapshot" || resp.Status != envelope.StatusSuccess {
				t.Fatalf("unexpected response while blocked: %+v", resp)
			}
		case <-time.After(time.Second):
			t.Fatal("busy command queue blocked snapshot admission")
		}
	}
	if len(mutations) != 1 {
		t.Fatal("mutations ran concurrently")
	}
	// Release the first request without closing release (deferred cleanup owns it).
	release <- struct{}{}
	for i := -1; i < requestQueueSize; i++ {
		want := fmt.Sprint(i)
		if i == -1 {
			want = "first"
		}
		select {
		case id := <-mutations:
			if id != want {
				t.Fatalf("mutation order = %s, want %s", id, want)
			}
		case <-time.After(time.Second):
			t.Fatal("queued mutations did not resume")
		}
	}
}

func TestStdioDrainsResponsesAfterInputEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := log.New(io.Discard, "", 0)
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := handler.New(st, agent.New(logger), "test", t.TempDir())
	d := newRequestDispatcher(ctx, h.Handle, logger)
	var output bytes.Buffer
	runStdio(ctx, h, d, logger, strings.NewReader(`{"type":"request","method":"get_version","request_id":"version","data":{}}`+"\n"), &output, true)
	var response envelope.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("invalid response after EOF: %s: %v", output.String(), err)
	}
	if response.RequestID != "version" || response.Status != envelope.StatusSuccess {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestDispatcherSkipsDisconnectedRequestsAndKeepsMutationOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldConnection, disconnect := context.WithCancel(ctx)
	defer disconnect()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	executed := make(chan string, 3)
	d := newRequestDispatcher(ctx, func(_ context.Context, cmd *envelope.Command) *envelope.Response {
		executed <- cmd.RequestID
		if cmd.RequestID == "old-running" {
			close(started)
			<-release // Models a handler that does not honor cancellation.
		}
		return envelope.SuccessResponse(cmd.RequestID, nil)
	}, log.New(io.Discard, "", 0))
	responses := make(chan string, 3)
	respond := func(resp *envelope.Response) { responses <- resp.RequestID }
	done := make(chan struct{}, 3)
	finished := func() { done <- struct{}{} }
	d.submit(oldConnection, &envelope.Command{Method: "execute_command", RequestID: "old-running"}, respond, finished)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old request did not start")
	}
	d.submit(oldConnection, &envelope.Command{Method: "execute_command", RequestID: "old-queued"}, respond, finished)
	disconnect()
	d.submit(ctx, &envelope.Command{Method: "execute_command", RequestID: "new-connection"}, respond, finished)
	if len(executed) != 1 {
		t.Fatal("new connection overlapped an old mutation")
	}
	release <- struct{}{}
	for range 3 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("requests did not complete")
		}
	}
	if len(executed) != 2 || <-executed != "old-running" || <-executed != "new-connection" {
		t.Fatal("a disconnected queued mutation ran")
	}
	if len(responses) != 1 || <-responses != "new-connection" {
		t.Fatal("a disconnected request sent a late response")
	}
}
