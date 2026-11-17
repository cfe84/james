package agent

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerReservationRejectsDuplicateAndPreservesReplacementOwner(t *testing.T) {
	r := New(nil)
	first := exec.Command("first")
	second := exec.Command("second")
	firstActivity := newActivityBuffer(1)
	secondActivity := newActivityBuffer(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !r.reserveProcess("session", first, firstActivity, cancel) {
		t.Fatal("first reservation failed")
	}
	if r.reserveProcess("session", second, secondActivity, cancel) {
		t.Fatal("duplicate reservation succeeded")
	}
	r.releaseProcess("session", second)
	if !r.IsRunning("session") {
		t.Fatal("non-owner cleanup removed active reservation")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- r.Stop("session") }()
	<-ctx.Done()
	if r.reserveProcess("session", second, secondActivity, cancel) {
		t.Fatal("stopping run released its reservation before exit")
	}
	r.releaseProcess("session", first)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if !r.reserveProcess("session", second, secondActivity, cancel) {
		t.Fatal("replacement reservation failed")
	}
	r.releaseProcess("session", first)
	if !r.IsRunning("session") {
		t.Fatal("old owner cleanup removed replacement reservation")
	}
	if got := r.GetActivity("session"); got == nil {
		t.Fatal("old owner cleanup removed replacement activity")
	}
	r.releaseProcess("session", second)
	if r.IsRunning("session") {
		t.Fatal("owner cleanup did not release reservation")
	}
	if got := r.GetActivity("session"); got != nil {
		t.Fatal("current owner cleanup left activity")
	}
}

func TestAgentProcessTreeHelper(t *testing.T) {
	switch os.Getenv("JAMES_TEST_PROCESS_TREE") {
	case "launcher":
		child := exec.Command(os.Args[0], "-test.run=^TestAgentProcessTreeHelper$")
		child.Env = append(os.Environ(), "JAMES_TEST_PROCESS_TREE=child")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			os.Exit(1)
		}
	case "child":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		data, err := json.Marshal(struct {
			Address string
			PID     int
		}{listener.Addr().String(), os.Getpid()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("JAMES_TEST_PROCESS_READY"), data, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	}
}

func TestRunnerCancellationKillsLauncherDescendants(t *testing.T) {
	for _, mode := range []string{"stop", "context", "before-start"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := filepath.Join(t.TempDir(), "ready.json")
			cmd := agentCommand(ctx, os.Args[0], "-test.run=^TestAgentProcessTreeHelper$")
			cmd.Env = append(os.Environ(), "JAMES_TEST_PROCESS_TREE=launcher", "JAMES_TEST_PROCESS_READY="+ready)
			r := New(log.New(io.Discard, "", 0))
			if !r.reserveProcess("session", cmd, newActivityBuffer(1), cancel) {
				t.Fatal("reservation failed")
			}
			if mode == "before-start" {
				cancel()
			}
			finished := make(chan error, 1)
			go func() {
				_, err := r.runCopilotStreaming(cmd, newActivityBuffer(1), "session", newBoundedBuffer(1024), false)
				r.releaseProcess("session", cmd)
				finished <- err
			}()
			var child struct {
				Address string
				PID     int
			}
			childExited := false
			if mode != "before-start" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					data, err := os.ReadFile(ready)
					if err == nil && json.Unmarshal(data, &child) == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("launcher descendant did not become ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Cleanup(func() {
					if !childExited {
						if process, err := os.FindProcess(child.PID); err == nil {
							_ = process.Kill()
						}
					}
				})
				if mode == "stop" {
					if err := r.Stop("session"); err != nil {
						t.Fatal(err)
					}
				} else {
					cancel()
				}
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("canceled agent reported success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled launcher or descendant kept the stream open")
			}
			if r.IsRunning("session") {
				t.Fatal("completed run retained its reservation")
			}
			if mode == "before-start" {
				if cmd.Process != nil {
					t.Fatal("canceled reservation started a process")
				}
				return
			}
			// A descendant-owned listener models a session lock surviving its launcher.
			deadline := time.Now().Add(5 * time.Second)
			for {
				listener, err := net.Listen("tcp", child.Address)
				if err == nil {
					_ = listener.Close()
					childExited = true
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant kept its session resource: %v", err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
