package handler

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCopilotModelLog(t *testing.T) {
	dir := t.TempDir()
	logLine := `2026-06-10T19:13:29.906Z [DEBUG] Listed models: [claude-opus-4.8,Claude Opus 4.8], [claude-sonnet-4.6,Claude Sonnet 4.6], [gpt-5.5,GPT-5.5], [text-embedding-3-small,Embedding V3 small], [gpt-4o,GPT-4o], [gpt-4o,GPT-4o]`
	if err := os.WriteFile(filepath.Join(dir, "process-1.log"), []byte("noise line\n"+logLine+"\nmore noise\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	models := parseCopilotModelLog(dir)
	if len(models) != 4 {
		t.Fatalf("expected 4 models (embeddings filtered, dupes removed), got %d: %+v", len(models), models)
	}

	want := []struct{ value, name string }{
		{"claude-opus-4.8", "Claude Opus 4.8"},
		{"claude-sonnet-4.6", "Claude Sonnet 4.6"},
		{"gpt-5.5", "GPT-5.5"},
		{"gpt-4o", "GPT-4o"},
	}
	for i, w := range want {
		if models[i].Value != w.value || models[i].Name != w.name {
			t.Errorf("model %d: got {Value:%q Name:%q}, want {Value:%q Name:%q}",
				i, models[i].Value, models[i].Name, w.value, w.name)
		}
	}
}

func TestParseCopilotModelLogNoLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "process-1.log"), []byte("nothing to see here\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if got := parseCopilotModelLog(dir); got != nil {
		t.Errorf("expected nil for log without model line, got %+v", got)
	}
}

func TestParseCopilotModelLogEmptyDir(t *testing.T) {
	if got := parseCopilotModelLog(t.TempDir()); got != nil {
		t.Errorf("expected nil for empty dir, got %+v", got)
	}
}
