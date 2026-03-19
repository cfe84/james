//go:build !windows

package updater

import (
	"os"
	"syscall"
)

// reExec replaces the current process with a new instance of the binary.
func reExec(binary string, args []string) error {
	return syscall.Exec(binary, args, os.Environ())
}
