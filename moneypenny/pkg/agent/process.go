package agent

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
)

func agentCommand(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	configureAgentProcess(cmd)
	cmd.Cancel = func() error {
		err := killAgentProcessTree(cmd)
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("agent process tree cleanup failed: pid=%d error=%v", cmd.Process.Pid, err)
		}
		return err
	}
	return cmd
}
