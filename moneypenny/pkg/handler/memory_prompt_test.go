package handler

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/memory"
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
	dir := filepath.Join(t.TempDir(), "memory")
	for _, text := range []string{
		"# Project\n\n[Architecture](architecture/README.md): read before changing the protocol.\n",
		"# Updated project\n\n[Architecture](architecture/README.md): transport and data ownership.\n",
	} {
		if err := memory.SetBatch(dir, []*memory.Node{
			{Path: "", Body: text},
			{Path: "architecture", Body: "# DescendantOnly\n\nDetailed child knowledge."},
		}); err != nil {
			t.Fatal(err)
		}
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
				dir := filepath.Join(t.TempDir(), "memory")
				text := strings.Repeat(char, count)
				writePromptMemory(t, dir, text)
				if err := memory.Migrate(dir, nil); err != nil {
					t.Fatal(err)
				}
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
				if count > 4000 && !strings.Contains(prompt, "gadgets memory get to read the full root") {
					t.Fatal("missing full-node command")
				}
				saved, err := memory.Get(dir, "")
				if err != nil || saved.Body != text {
					t.Fatal("injection changed authoritative content")
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
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("seeding created legacy memory file: %v", err)
	}
	node, err := memory.Get(dir, "")
	if err != nil || node == nil || node.Body != rootReadmeTemplate {
		t.Fatalf("root was not persisted in SQLite: %+v, %v", node, err)
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
			h, _ := newMemoryAuxiliaryTestHandler(t, name)
			dir := filepath.Join(h.dataDir, "sessions", sid, "memory")
			writePromptMemory(t, dir, "# Durable knowledge")
			params := agent.RunParams{Agent: name, Yolo: yolo, SystemPrompt: "Base instructions"}
			if err := h.prepareRunInstructions(sid, &params); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(params.SystemPrompt, "Base instructions") {
				t.Fatal("lost configured instructions")
			}
			if !strings.Contains(params.SystemPrompt, "gadgets notify") {
				t.Fatalf("%s yolo=%t lacks notification guidance", name, yolo)
			}
			if !strings.Contains(params.SystemPrompt, "<session-memory>") {
				t.Fatalf("%s yolo=%t: memory access/injection mismatch", name, yolo)
			}
		}
	}
}

func TestMemoryPromptUsesSQLiteAfterMigration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	writePromptMemory(t, dir, "# Legacy")
	if err := memory.Migrate(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Set(dir, "", "# Authoritative"); err != nil {
		t.Fatal(err)
	}
	writePromptMemory(t, dir, "# Stale backup")
	prompt, err := memorySystemPrompt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if injectedRoot(t, prompt) != "# Authoritative" {
		t.Fatal("injected stale file instead of authoritative SQLite root")
	}
	for _, command := range []string{"get", "list", "search", "set", "batch", "delete", "revisions"} {
		if !strings.Contains(prompt, "gadgets memory "+command) {
			t.Errorf("missing gadget command %s", command)
		}
	}
	for _, obsolete := range []string{dir, "native file tools to read and edit", "child's README.md"} {
		if strings.Contains(prompt, obsolete) {
			t.Errorf("injected obsolete memory instruction %q", obsolete)
		}
	}
}

func TestMemoryPromptWarnsAboutOversizedDescendants(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "memory")
	if err := memory.Migrate(dir, []*memory.Node{
		{Path: "", Body: "# Small root"},
		{Path: "topic", Body: strings.Repeat("é", 4100)},
		{Path: "topic/child", Body: strings.Repeat("🕴", 4200)},
	}); err != nil {
		t.Fatal(err)
	}
	prompt, err := memorySystemPrompt(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"topic": 4100 Unicode characters`, `"topic/child": 4200 Unicode characters`, "retaining useful knowledge"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(prompt, strings.Repeat("é", 100)) {
		t.Fatal("descendant body injected")
	}
}

func TestOversizedMemoryWarningIsBoundedAndPerNode(t *testing.T) {
	nodes := []memory.Size{
		{Path: "", Characters: 3000},
		{Path: "child", Characters: 3000},
	}
	if warning := oversizedMemoryWarning(nodes); warning != "" {
		t.Fatalf("aggregate size must not trigger warning: %s", warning)
	}
	for i := 0; i < 100; i++ {
		nodes = append(nodes, memory.Size{Path: strings.Repeat("long/", 100) + strconv.Itoa(i), Characters: 4001 + i})
	}
	warning := oversizedMemoryWarning(nodes)
	if utf8.RuneCountInString(warning) > memoryOversizedWarningCharacters {
		t.Fatalf("warning exceeds bound: %d", utf8.RuneCountInString(warning))
	}
	if !strings.Contains(warning, "100 total") || !strings.Contains(warning, "additional oversized nodes omitted") {
		t.Fatalf("missing bounded listing details: %s", warning)
	}
}
