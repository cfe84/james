package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

var errOpenCodeSessionMissing = errors.New("opencode session metadata not found")

// openCodeSessionDirectory reads the directory bound to an existing OpenCode
// session. Resuming from another directory can leave its event stream silent.
func openCodeSessionDirectory(ctx context.Context, binary, sessionID, workingDir string, env []string) (string, error) {
	if !strings.HasPrefix(sessionID, "ses_") {
		return "", errors.New("invalid opencode session ID")
	}
	for _, ch := range sessionID {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-') {
			return "", errors.New("invalid opencode session ID")
		}
	}

	query := fmt.Sprintf("select directory from session where id = '%s'", sessionID)
	cmd := exec.CommandContext(ctx, binary, "db", query, "--format", "json")
	cmd.Dir = workingDir
	cmd.Env = env
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("query opencode session metadata: %w", err)
	}
	if len(output) > 4096 {
		return "", errors.New("opencode session metadata response exceeds limit")
	}
	var sessions []struct {
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(output, &sessions); err != nil {
		return "", fmt.Errorf("decode opencode session metadata: %w", err)
	}
	if len(sessions) == 0 {
		return "", errOpenCodeSessionMissing
	}
	if len(sessions) != 1 || !filepath.IsAbs(sessions[0].Directory) {
		return "", errors.New("invalid opencode session directory")
	}
	return filepath.Clean(sessions[0].Directory), nil
}
