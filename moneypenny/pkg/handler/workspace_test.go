package handler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/envelope"
)

func TestSessionWorkspaceLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	h, _ := gadgetTestHandler(t)
	t.Cleanup(h.Close)
	started := make(chan string, 2)
	h.runAgentFunc = func(_ context.Context, params agent.RunParams) (*agent.Result, error) {
		started <- params.Path
		return &agent.Result{}, nil
	}
	repo := filepath.Join(home, "james")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init")
	git(repo, "-c", "user.name=Tester", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "initial")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, test := range []struct {
		id      string
		scratch bool
	}{
		{"00000000-0000-0000-0000-000000000073", true},
		{"00000000-0000-0000-0000-000000000074", false},
	} {
		data := envelope.CreateSessionData{SessionID: test.id, Name: "test", Agent: "git", Prompt: "test", NoFolder: test.scratch}
		if !test.scratch {
			data.Worktree = &envelope.WorktreeData{Repository: repo, Folder: "jamy", Branch: "features/jamy"}
		}
		raw, _ := json.Marshal(data)
		resp := h.createSession(ctx, &envelope.Command{Data: raw})
		if resp.Status != envelope.StatusSuccess {
			t.Fatalf("create failed: %#v", resp)
		}
		root, err := scratchRoot()
		if err != nil {
			t.Fatal(err)
		}
		wantPath := filepath.Join(root, test.id)
		if !test.scratch {
			wantPath = filepath.Join(home, "jamy")
			if got := git(wantPath, "branch", "--show-current"); got != "features/jamy" || git(wantPath, "rev-parse", "HEAD") != git(repo, "rev-parse", "HEAD") {
				t.Fatalf("worktree branch or HEAD incorrect: %s", got)
			}
			if _, err := createSessionWorktree(ctx, data.Worktree); err == nil || !strings.Contains(err.Error(), "destination already exists") {
				t.Fatalf("destination collision: %v", err)
			}
			other := *data.Worktree
			other.Folder = "collision"
			if _, err := createSessionWorktree(ctx, &other); err == nil {
				t.Fatal("reused an existing branch")
			}
			if _, err := os.Stat(filepath.Join(home, other.Folder)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed Git operation left its empty destination: %v", err)
			}
		}
		select {
		case got := <-started:
			if got != wantPath {
				t.Fatalf("agent path %q, want %q", got, wantPath)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		raw, _ = json.Marshal(envelope.SessionIDData{SessionID: test.id})
		if resp := h.deleteSession(ctx, &envelope.Command{Data: raw}); resp.Status != envelope.StatusSuccess {
			t.Fatalf("delete failed: %#v", resp)
		}
		if _, err := os.Stat(wantPath); (test.scratch && !errors.Is(err, os.ErrNotExist)) || (!test.scratch && err != nil) {
			t.Fatalf("unexpected workspace after deletion (scratch=%t): %v", test.scratch, err)
		}
	}
}
