package agent

import (
	"strings"
	"testing"
)

func argsContainPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func argsContain(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

func TestMemoryAccessArgsClaude(t *testing.T) {
	mem := "/data/sessions/s1/memory"
	// Non-yolo: add-dir + scoped allowedTools.
	args := memoryAccessArgs("claude", RunParams{MemoryDir: mem})
	if !argsContainPair(args, "--add-dir", mem) {
		t.Errorf("claude non-yolo missing --add-dir: %v", args)
	}
	joined := strings.Join(args, " ")
	// Absolute memory path is anchored with a doubled leading slash so Claude
	// treats it as a filesystem path rather than project-root-relative.
	if !strings.Contains(joined, "--allowedTools") || !strings.Contains(joined, "Write(/"+mem+"/**)") {
		t.Errorf("claude non-yolo missing scoped write perm: %v", args)
	}
	// Yolo: only add-dir, no allowedTools (everything already permitted).
	yargs := memoryAccessArgs("claude", RunParams{MemoryDir: mem, Yolo: true})
	if !argsContainPair(yargs, "--add-dir", mem) {
		t.Errorf("claude yolo missing --add-dir: %v", yargs)
	}
	if argsContain(yargs, "--allowedTools") {
		t.Errorf("claude yolo should not add allowedTools: %v", yargs)
	}
	// No memory dir: no args.
	if got := memoryAccessArgs("claude", RunParams{}); len(got) != 0 {
		t.Errorf("expected no args without MemoryDir, got %v", got)
	}
}

func TestMemoryAccessArgsCopilot(t *testing.T) {
	mem := "/data/sessions/s1/memory"
	// Non-yolo Copilot: memory is disabled entirely (Copilot can't path-scope
	// writes, and we won't grant broad write to a non-yolo session).
	args := memoryAccessArgs("copilot", RunParams{MemoryDir: mem})
	if len(args) != 0 {
		t.Errorf("copilot non-yolo should get no memory args, got %v", args)
	}
	if MemoryEnabled("copilot", false) {
		t.Errorf("MemoryEnabled(copilot, non-yolo) should be false")
	}
	// Yolo Copilot: everything already permitted, just expose the folder.
	yargs := memoryAccessArgs("copilot", RunParams{MemoryDir: mem, Yolo: true})
	if !argsContainPair(yargs, "--add-dir", mem) {
		t.Errorf("copilot yolo missing --add-dir: %v", yargs)
	}
	if argsContain(yargs, "--allow-tool=write") {
		t.Errorf("copilot yolo should not add allow-tool: %v", yargs)
	}
	if !MemoryEnabled("copilot", true) {
		t.Errorf("MemoryEnabled(copilot, yolo) should be true")
	}
}

func TestBuildArgsIncludesMemoryDir(t *testing.T) {
	mem := "/data/sessions/s1/memory"
	// Copilot needs yolo for memory to be wired up at all.
	cInv := buildCopilotArgs(RunParams{Agent: "copilot", Prompt: "hi", MemoryDir: mem, Yolo: true})
	if !argsContainPair(cInv.args, "--add-dir", mem) {
		t.Errorf("buildCopilotArgs missing --add-dir: %v", cInv.args)
	}
	// Non-yolo Copilot: no --add-dir (memory disabled).
	cInvNo := buildCopilotArgs(RunParams{Agent: "copilot", Prompt: "hi", MemoryDir: mem})
	if argsContainPair(cInvNo.args, "--add-dir", mem) {
		t.Errorf("buildCopilotArgs non-yolo should not add memory --add-dir: %v", cInvNo.args)
	}
	clInv := buildClaudeArgs(RunParams{Agent: "claude", Prompt: "hi", MemoryDir: mem})
	if !argsContainPair(clInv.args, "--add-dir", mem) {
		t.Errorf("buildClaudeArgs missing --add-dir: %v", clInv.args)
	}
}
