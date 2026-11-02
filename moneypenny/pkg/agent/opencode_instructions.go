package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func prepareOpenCodeInstructions(env []string, prompt, sessionDir string) ([]string, func(), error) {
	cleanup := func() {}
	if prompt == "" {
		return env, cleanup, nil
	}
	config := map[string]json.RawMessage{}
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "OPENCODE_CONFIG_CONTENT") {
			result = append(result, entry)
			continue
		}
		if value == "" {
			continue
		}
		if err := json.Unmarshal([]byte(value), &config); err != nil || config == nil {
			return nil, cleanup, fmt.Errorf("OpenCode instructions require OPENCODE_CONFIG_CONTENT to be a JSON object")
		}
	}
	var instructions []string
	if raw, ok := config["instructions"]; ok {
		if err := json.Unmarshal(raw, &instructions); err != nil || instructions == nil {
			return nil, cleanup, fmt.Errorf("OpenCode config instructions must be an array of file paths")
		}
	}
	dir := sessionDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "james-opencode-instructions-")
		if err != nil {
			return nil, cleanup, fmt.Errorf("create OpenCode instructions directory: %w", err)
		}
		cleanup = func() {
			if err := os.RemoveAll(dir); err != nil {
				fmt.Fprintf(os.Stderr, "remove temporary OpenCode instructions: %v\n", err)
			}
		}
	}
	fail := func(err error) ([]string, func(), error) {
		cleanup()
		return nil, func() {}, err
	}
	path, err := filepath.Abs(filepath.Join(dir, "opencode-instructions.md"))
	if err != nil {
		return fail(fmt.Errorf("resolve OpenCode instructions path: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fail(fmt.Errorf("create OpenCode instructions directory: %w", err))
	}
	// Rewrite even on resumes so traits, permissions and memory never become stale.
	if err := os.WriteFile(path, []byte(prompt), 0600); err != nil {
		return fail(fmt.Errorf("write OpenCode instructions: %w", err))
	}
	instructions = append(instructions, filepath.ToSlash(path))
	config["instructions"], err = json.Marshal(instructions)
	if err != nil {
		return fail(fmt.Errorf("encode OpenCode instructions: %w", err))
	}
	content, err := json.Marshal(config)
	if err != nil {
		return fail(fmt.Errorf("encode OpenCode config: %w", err))
	}
	result = append(result, "OPENCODE_CONFIG_CONTENT="+string(content))
	return result, cleanup, nil
}
