package handler

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
)

func writePromptMemory(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func injectedRoot(t *testing.T, prompt string) string {
	t.Helper()
	_, root, ok := strings.Cut(prompt, "<root-memory>\n")
	if !ok {
		t.Fatal("missing injected root")
	}
	root, _, ok = strings.Cut(root, "\n</root-memory>")
	if !ok {
		t.Fatal("missing root closing marker")
	}
	return root
}

func TestMemoryPromptInjectsFreshRootOnly(t *testing.T) {
	dir := t.TempDir()
	for _, text := range []string{
		"# Project\n\n[Architecture](architecture/README.md): read before changing the protocol.\n",
		"# Updated project\n\n[Architecture](architecture/README.md): transport and data ownership.\n",
	} {
		writePromptMemory(t, dir, text)
		writePromptMemory(t, filepath.Join(dir, "architecture"), "# DescendantOnly\n\nDetailed child knowledge.")
		prompt, err := memorySystemPrompt(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := injectedRoot(t, prompt); got != text {
			t.Fatalf("injected root = %q, want %q", got, text)
		}
		if strings.Contains(prompt, "DescendantOnly") || strings.Contains(prompt, "Current memory tree:") {
			t.Fatal("descendant outline/body was injected")
		}
		for _, instruction := range []string{
			"4000 Unicode characters", "2000 or fewer", "split its details",
			"annotated index", "when to consult", "not these memory-use instructions",
			"parent index", "before handoff or compaction",
		} {
			if !strings.Contains(prompt, instruction) {
				t.Errorf("missing instruction %q", instruction)
			}
		}
	}
}

func TestMemoryPromptRootCharacterBudget(t *testing.T) {
	for _, char := range []string{"a", "é", "🕴"} {
		for _, count := range []int{3999, 4000, 4001, 20000} {
			t.Run(char+"/"+strconv.Itoa(count), func(t *testing.T) {
				dir := t.TempDir()
				text := strings.Repeat(char, count)
				writePromptMemory(t, dir, text)
				prompt, err := memorySystemPrompt(dir)
				if err != nil {
					t.Fatal(err)
				}
				root := injectedRoot(t, prompt)
				want := min(count, 4000)
				if !utf8.ValidString(root) || utf8.RuneCountInString(root) != want || root != strings.Repeat(char, want) {
					t.Fatalf("wrong Unicode bound: %d characters, want %d", utf8.RuneCountInString(root), want)
				}
				if strings.Contains(prompt, "Root excerpt truncated") != (count > 4000) {
					t.Fatal("truncation notice does not match overflow")
				}
				if count > 4000 && !strings.Contains(prompt, filepath.Join(dir, "README.md")+" before restructuring") {
					t.Fatal("missing full-file pointer")
				}
				saved, err := os.ReadFile(filepath.Join(dir, "README.md"))
				if err != nil || string(saved) != text {
					t.Fatal("injection changed the file")
				}
			})
		}
	}
}

func TestMemoryPromptSeedsKnowledgeOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	prompt, err := memorySystemPrompt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := injectedRoot(t, prompt); got != rootReadmeTemplate {
		t.Fatalf("unexpected new root: %q", got)
	}
	for _, instruction := range []string{"Read this", "Organize", "instructions", "4000", "Each node"} {
		if strings.Contains(rootReadmeTemplate, instruction) {
			t.Fatalf("root contains usage instruction %q", instruction)
		}
	}
}

func TestMemoryPromptReportsIOFailures(t *testing.T) {
	t.Run("memory path is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "memory")
		if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if prompt, err := memorySystemPrompt(path); err == nil || prompt != "" {
			t.Fatalf("got (%q, %v), want explicit error", prompt, err)
		}
	})
	t.Run("root is a directory", func(t *testing.T) {
		path := t.TempDir()
		if err := os.Mkdir(filepath.Join(path, "README.md"), 0700); err != nil {
			t.Fatal(err)
		}
		if prompt, err := memorySystemPrompt(path); err == nil || prompt != "" {
			t.Fatalf("got (%q, %v), want explicit error", prompt, err)
		}
	})
}

func TestRunInstructionsPermissionsAndNotifications(t *testing.T) {
	const sid = "123e4567-e89b-12d3-a456-426614174000"
	for _, name := range []string{"claude", "copilot", "opencode"} {
		for _, yolo := range []bool{false, true} {
			h := &Handler{dataDir: t.TempDir()}
			dir := filepath.Join(h.dataDir, "sessions", sid, "memory")
			// Existing memory bypasses legacy migration; no store or agent process is needed.
			writePromptMemory(t, dir, "# Durable knowledge")
			params := agent.RunParams{Agent: name, Yolo: yolo, SystemPrompt: "Base instructions"}
			if err := h.prepareRunInstructions(sid, &params); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(params.SystemPrompt, "Base instructions") {
				t.Fatal("lost configured instructions")
			}
			if !strings.Contains(params.SystemPrompt, notifyUserSystemPromptSuffix) {
				t.Fatalf("%s yolo=%t lacks notification guidance", name, yolo)
			}
			enabled := name == "claude" || yolo
			if strings.Contains(params.SystemPrompt, "<session-memory>") != enabled || (params.MemoryDir != "") != enabled {
				t.Fatalf("%s yolo=%t: memory access/injection mismatch", name, yolo)
			}
		}
	}
}
