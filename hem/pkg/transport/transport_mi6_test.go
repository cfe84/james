//go:build !windows

package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMI6ClientHelper runs in a child process in place of mi6-client.
func TestMI6ClientHelper(t *testing.T) {
	if os.Getenv("HEM_TEST_MI6_CLIENT") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(1)
	}
	var cmd Command
	if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
		os.Exit(2)
	}
	dir := os.Getenv("HEM_TEST_MI6_DIR")
	if cmd.Method == "slow" {
		if os.WriteFile(filepath.Join(dir, "started"), nil, 0600) != nil {
			os.Exit(3)
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	encoder := json.NewEncoder(os.Stdout)
	// Simulate other callers' requests, notifications and responses in the relay.
	encoder.Encode(Response{Type: "request", RequestID: cmd.RequestID})
	encoder.Encode(Response{Type: "notification", RequestID: cmd.RequestID})
	encoder.Encode(Response{Type: "response", RequestID: "another-request", Status: "ok"})
	encoder.Encode(Response{Type: "response", Status: "ok"})
	switch cmd.Method {
	case "eof":
		os.Exit(0)
	case "malformed":
		fmt.Println("not-json")
	default:
		data, _ := json.Marshal(cmd.Method)
		status := "ok"
		if cmd.Method == "error" {
			status = "error"
		}
		encoder.Encode(Response{Type: "response", RequestID: cmd.RequestID, Status: status, Data: data})
	}
	// Like mi6-client, keep running until the caller closes stdin.
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func fakeMI6Client(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run='^TestMI6ClientHelper$'\n"
	if err := os.WriteFile(filepath.Join(dir, "mi6-client"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HEM_TEST_MI6_CLIENT", "1")
	t.Setenv("HEM_TEST_MI6_DIR", dir)
	return dir
}

func TestMI6CorrelatesBroadcastResponses(t *testing.T) {
	fakeMI6Client(t)
	c := NewMI6Client("example/session", "key", "fingerprint")
	for _, method := range []string{"list_sessions", "error", "eof", "malformed"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := &Command{Type: "request", Method: method, RequestID: "this-request"}
			resp, err := c.Send(ctx, cmd)
			if method == "eof" || method == "malformed" {
				if err == nil {
					t.Fatalf("got unrelated response instead of error: %#v", resp)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.RequestID != cmd.RequestID || string(resp.Data) != `"`+method+`"` {
				t.Fatalf("received another request's response: %#v", resp)
			}
			if method == "error" && resp.Status != "error" {
				t.Fatalf("lost matching error response: %#v", resp)
			}
		})
	}
	if _, err := c.Send(context.Background(), &Command{Type: "request"}); err == nil {
		t.Fatal("uncorrelatable request accepted")
	}
}

func TestMI6SlowCommandDoesNotBlockDashboardRead(t *testing.T) {
	dir := fakeMI6Client(t)
	c := NewMI6Client("example/session", "key", "fingerprint")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	slowDone := make(chan error, 1)
	go func() {
		_, err := c.SendCommand(ctx, "slow", nil)
		slowDone <- err
	}()
	t.Cleanup(cancel)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slow request never reached the relay")
		}
		time.Sleep(time.Millisecond)
	}

	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()
	resp, err := c.SendCommand(readCtx, "list_sessions", nil)
	if err != nil {
		t.Fatalf("dashboard read blocked behind slow command: %v", err)
	}
	if string(resp.Data) != `"list_sessions"` {
		t.Fatalf("dashboard got wrong response: %#v", resp)
	}
	select {
	case err := <-slowDone:
		t.Fatalf("slow request should still be running: %v", err)
	default:
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("slow request failed to finish")
	}
}
