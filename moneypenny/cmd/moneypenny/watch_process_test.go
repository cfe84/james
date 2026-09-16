package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWatchLifecycleAcrossDaemonProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process signal and stdio behavior differs on Windows")
	}

	workDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	testDir, err := os.MkdirTemp(".", ".watch-process-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testDir) })

	binary := filepath.Join(testDir, "moneypenny")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = workDir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build moneypenny: %v\n%s", err, output)
	}
	dataDir := filepath.Join(testDir, "data")

	start := func() (*exec.Cmd, io.WriteCloser, *bufio.Scanner) {
		cmd := exec.Command(binary, "--data-dir", dataDir)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatalf("start moneypenny: %v", err)
		}
		return cmd, stdin, bufio.NewScanner(stdout)
	}
	send := func(stdin io.Writer, scanner *bufio.Scanner, request string) map[string]interface{} {
		if _, err := io.WriteString(stdin, request+"\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
		result := make(chan map[string]interface{}, 1)
		go func() {
			if !scanner.Scan() {
				result <- map[string]interface{}{"_scanner_error": scanner.Err()}
				return
			}
			var response map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				result <- map[string]interface{}{"_json_error": err}
				return
			}
			result <- response
		}()
		select {
		case response := <-result:
			if err, ok := response["_scanner_error"].(error); ok {
				t.Fatalf("read response: %v", err)
			}
			if err, ok := response["_json_error"].(error); ok {
				t.Fatalf("decode response: %v", err)
			}
			return response
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for response")
			return nil
		}
	}
	watchRequest := func(method, id, connection string, epoch int) string {
		return `{"type":"request","method":"` + method + `","request_id":"` + id +
			`","data":{"watch_id":"watch-1","connection_id":"` + connection +
			`","epoch":` + strconv.Itoa(epoch) + `,"session_id":"session-1"}}`
	}
	assertSuccess := func(response map[string]interface{}, requestID string) {
		if response["type"] != "response" || response["status"] != "success" ||
			response["request_id"] != requestID {
			t.Fatalf("response = %#v", response)
		}
	}

	cmd, stdin, scanner := start()
	assertSuccess(send(stdin, scanner, watchRequest("watch_session", "open", "connection-a", 1)), "open")
	assertSuccess(send(stdin, scanner, watchRequest("renew_watch", "renew", "connection-a", 1)), "renew")
	assertSuccess(send(stdin, scanner, watchRequest("unwatch_session", "close", "connection-a", 1)), "close")
	assertSuccess(send(stdin, scanner, watchRequest("unwatch_session", "duplicate", "connection-a", 1)), "duplicate")
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("clean daemon exit: %v", err)
	}

	// A downstream process can disappear without sending unwatch. The watch
	// registry is intentionally process-local, so a restarted daemon must not
	// inherit the old lease or reject the same identity/epoch.
	cmd, stdin, scanner = start()
	assertSuccess(send(stdin, scanner, watchRequest("watch_session", "reconnect", "connection-a", 2)), "reconnect")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("abrupt daemon disconnect: %v", err)
	}
	if err := cmd.Wait(); err != nil && !strings.Contains(err.Error(), "signal") {
		t.Fatalf("wait after abrupt disconnect: %v", err)
	}

	cmd, stdin, scanner = start()
	assertSuccess(send(stdin, scanner, watchRequest("watch_session", "after-restart", "connection-a", 3)), "after-restart")
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("restart daemon exit: %v", err)
	}
}
