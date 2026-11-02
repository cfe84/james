package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdaptersDeliverRefreshedMemoryInstructions(t *testing.T) {
	for _, name := range []string{"claude", "copilot", "opencode"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, resume := range []bool{false, true, false} {
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
					if text != "Task" {
						t.Fatalf("instructions leaked into task: %s", text)
					}
					env, cleanup, err := prepareOpenCodeInstructions(nil, prompt, dir)
					if err != nil {
						t.Fatal(err)
					}
					cleanup()
					var config struct{ Instructions []string }
					if err := json.Unmarshal([]byte(strings.TrimPrefix(env[0], "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
						t.Fatal(err)
					}
					if len(config.Instructions) != 1 || !filepath.IsAbs(config.Instructions[0]) {
						t.Fatalf("missing absolute instructions path: %+v", config)
					}
					body, err := os.ReadFile(config.Instructions[0])
					if err != nil || string(body) != prompt {
						t.Fatalf("stale/missing native instructions on resume=%t: %s, %v", resume, body, err)
					}
				}
			}
		})
	}
}

func TestOpenCodeInstructionConfigAndCleanup(t *testing.T) {
	dir := t.TempDir()
	env := []string{
		"KEEP=value", "JAMES_GADGETS_TOKEN=token", "JAMES_HEM_SOCKET=socket",
		"OPENCODE_CONFIG=custom.json", "OPENCODE_CONFIG_DIR=custom-dir",
		`OPENCODE_CONFIG_CONTENT={"instructions":["project.md"],"permission":{"bash":"deny"},"model":"provider/model"}`,
	}
	result, cleanup, err := prepareOpenCodeInstructions(env, "First session rules", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !reflect.DeepEqual(result[:len(result)-1], env[:len(env)-1]) {
		t.Fatalf("lost existing environment: %v", result)
	}
	var config struct {
		Instructions []string          `json:"instructions"`
		Permission   map[string]string `json:"permission"`
		Model        string            `json:"model"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(result[len(result)-1], "OPENCODE_CONFIG_CONTENT=")), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Instructions) != 2 || config.Instructions[0] != "project.md" ||
		config.Permission["bash"] != "deny" || config.Model != "provider/model" {
		t.Fatalf("existing config overwritten: %+v", config)
	}
	// One-shot work uses an isolated temporary file, even alongside a live session.
	temporary, remove, err := prepareOpenCodeInstructions(nil, "One-shot rules", "")
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	var tempConfig struct{ Instructions []string }
	if err := json.Unmarshal([]byte(strings.TrimPrefix(temporary[0], "OPENCODE_CONFIG_CONTENT=")), &tempConfig); err != nil {
		t.Fatal(err)
	}
	path := tempConfig.Instructions[0]
	if body, err := os.ReadFile(path); err != nil || string(body) != "One-shot rules" {
		t.Fatal("missing temporary instructions", err)
	}
	if body, err := os.ReadFile(config.Instructions[1]); err != nil || string(body) != "First session rules" {
		t.Fatal("another run overwrote session instructions", err)
	}
	remove()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("temporary instructions leaked", err)
	}
	unchanged, nothing, err := prepareOpenCodeInstructions(env, "", dir)
	nothing()
	if err != nil || !reflect.DeepEqual(unchanged, env) {
		t.Fatal("empty instructions changed config", err)
	}
	for _, oneShot := range []bool{false, true} {
		params := RunParams{Agent: "opencode", SystemPrompt: "Rules", Prompt: "--literal\nTask"}
		inv := buildArgs(params)
		if oneShot {
			inv = buildOneShotArgs(params)
		}
		if inv.args[len(inv.args)-1] != params.Prompt || inv.args[len(inv.args)-2] != "--" {
			t.Fatal("prompt changed or interpreted as flags", inv.args)
		}
	}
}

func TestOpenCodeInstructionPreparationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config, dir string
	}{
		{"invalid-config", "{secret", dir},
		{"null-config", "null", dir},
		{"invalid-instructions", `{"instructions":"secret"}`, dir},
		{"null-instructions", `{"instructions":null}`, dir},
		{"unwritable-directory", "{}", blocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cleanup, err := prepareOpenCodeInstructions([]string{"OPENCODE_CONFIG_CONTENT=" + tc.config}, "Rules", tc.dir)
			cleanup()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("preparation failure hidden or config leaked: %v", err)
			}
		})
	}
}
