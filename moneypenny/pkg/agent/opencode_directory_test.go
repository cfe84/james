package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	mode := os.Getenv("JAMES_TEST_OPENCODE_DIRECTORY")
	if mode == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) < 5 || os.Args[1] != "db" || !strings.Contains(os.Args[2], "ses_existing") ||
		os.Args[3] != "--format" || os.Args[4] != "json" {
		os.Exit(2)
	}
	switch mode {
	case "found":
		fmt.Fprintln(os.Stdout, `[{"directory":"/original"}]`)
	case "missing":
		fmt.Fprintln(os.Stdout, `[]`)
	case "invalid":
		fmt.Fprintln(os.Stdout, `[{"directory":"relative"}]`)
	case "failed":
		fmt.Fprintln(os.Stderr, "secret provider content")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestOpenCodeSessionDirectory(t *testing.T) {
	for _, tc := range []struct {
		mode, wantDir string
		wantMissing   bool
	}{
		{"found", "/original", false},
		{"missing", "", true},
		{"invalid", "", false},
		{"failed", "", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			env := append(os.Environ(), "JAMES_TEST_OPENCODE_DIRECTORY="+tc.mode)
			dir, err := openCodeSessionDirectory(context.Background(), os.Args[0], "ses_existing", "", env)
			if tc.wantDir != "" && (dir != tc.wantDir || err != nil) {
				t.Fatalf("directory = %q, error = %v", dir, err)
			}
			if tc.wantDir == "" && err == nil {
				t.Fatalf("expected error for %s", tc.mode)
			}
			if tc.wantMissing != errors.Is(err, errOpenCodeSessionMissing) {
				t.Fatalf("missing session classification: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("leaked process output: %v", err)
			}
		})
	}
	if _, err := openCodeSessionDirectory(context.Background(), os.Args[0], "ses_bad' OR 1=1", "", nil); err == nil {
		t.Fatal("invalid session ID accepted")
	}
}

func TestOpenCodeResumeUsesStoredDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a shell fixture")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = db ]; then
  printf '%s\n' '[{"directory":"/original"}]'
  exit 0
fi
case " $* " in
  *" --dir /original "*) ;;
  *) exit 12 ;;
esac
printf '%s\n' '{"type":"text","sessionID":"ses_existing","part":{"text":"answer"}}'
`
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runner := New(log.New(io.Discard, "", 0))
	result, err := runner.Run(context.Background(), RunParams{
		Agent: "opencode", SessionID: "test-session", AgentSessionID: "ses_existing",
		Resume: true, Path: t.TempDir(), Prompt: "secret prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "answer" || result.AgentSessionID != "ses_existing" {
		t.Fatalf("unexpected result: text length=%d session ID=%q", len(result.Text), result.AgentSessionID)
	}
	_, err = runner.Run(context.Background(), RunParams{
		Agent: "opencode", SessionID: "test-session", Resume: true,
		Path: t.TempDir(), Prompt: "do not silently restart",
	})
	var failure *OpenCodeFailure
	if !errors.As(err, &failure) || failure.Category != "session_not_found" {
		t.Fatalf("unresolved resume did not request history recovery: %v", err)
	}
}
