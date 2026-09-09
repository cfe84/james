package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptersDeliverRefreshedMemoryInstructions(t *testing.T) {
	for _, name := range []string{"claude", "copilot", "opencode"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, resume := range []bool{false, true} {
				root := "Original overview"
				if resume {
					root = "Updated overview"
				}
				prompt := "<session-memory>\nRules\n<root-memory>" + root + "</root-memory>\n</session-memory>"
				params := RunParams{
					Agent: name, SessionID: "session", AgentSessionID: "session",
					SessionDir: dir, SystemPrompt: prompt, Prompt: "Task", Resume: resume,
				}
				inv := buildArgs(params)
				switch name {
				case "claude":
					if !argsContainPair(inv.args, "--system-prompt", prompt) {
						t.Fatalf("missing instructions on resume=%t", resume)
					}
				case "copilot":
					instructionsDir := filepath.Join(dir, "copilot-instructions")
					if !argsContain(inv.env, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS="+instructionsDir) {
						t.Fatal("missing instructions environment")
					}
					body, err := os.ReadFile(filepath.Join(instructionsDir, ".github", "instructions", "system.instructions.md"))
					if err != nil || string(body) != prompt {
						t.Fatalf("stale/missing instructions on resume=%t: %s, %v", resume, body, err)
					}
				case "opencode":
					text := inv.args[len(inv.args)-1]
					if !strings.Contains(text, prompt) || !strings.HasSuffix(text, "Task") {
						t.Fatalf("missing task-prefix instructions: %s", text)
					}
				}
			}
		})
	}
}
