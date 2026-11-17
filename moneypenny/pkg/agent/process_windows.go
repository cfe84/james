package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

func configureAgentProcess(cmd *exec.Cmd) {}

func killAgentProcessTree(cmd *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	if err == nil {
		return nil
	}
	handle, openErr := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if openErr == nil {
		defer windows.CloseHandle(handle)
		if status, waitErr := windows.WaitForSingleObject(handle, 0); waitErr == nil && status == windows.WAIT_OBJECT_0 {
			return os.ErrProcessDone
		}
	}
	return fmt.Errorf("terminate agent process tree: %w", err)
}
