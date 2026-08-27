package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPruneLogFileKeepsNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moneypenny.log")
	var content strings.Builder
	for i := range maxLogLines {
		fmt.Fprintf(&content, "line-%05d\n", i)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if err := pruneLogFile(path); err != nil {
		t.Fatalf("pruneLogFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pruned log: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != maxLogLines-logLinesToDiscard {
		t.Fatalf("line count = %d, want %d", len(lines), maxLogLines-logLinesToDiscard)
	}
	if lines[0] != "line-01000" {
		t.Errorf("first line = %q, want %q", lines[0], "line-01000")
	}
	if lines[len(lines)-1] != "line-09999" {
		t.Errorf("last line = %q, want %q", lines[len(lines)-1], "line-09999")
	}
}

func TestPruneLogFileLeavesShortLogUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moneypenny.log")
	want := "first\nsecond\n"
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if err := pruneLogFile(path); err != nil {
		t.Fatalf("pruneLogFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}
