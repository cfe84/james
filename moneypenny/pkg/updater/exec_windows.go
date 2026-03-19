//go:build windows

package updater

import (
	"os"
	"os/exec"
)

// reExec on Windows starts a new process and exits the current one.
// Windows does not support in-place exec, so we spawn and exit.
func reExec(binary string, args []string) error {
	cmd := exec.Command(binary, args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
