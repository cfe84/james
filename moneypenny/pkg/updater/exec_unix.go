//go:build !windows

package updater

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// reExec replaces the current process with a new instance of the binary.
func reExec(binary string, args []string) error {
	return syscall.Exec(binary, args, os.Environ())
}

func installStagedUpdate(stagedDir, currentExe, currentMI6, currentHem string, args []string, beforeRestart func(), vlog *log.Logger) error {
	suffix := exeSuffix()
	newMI6 := filepath.Join(stagedDir, "mi6-client"+suffix)
	if _, err := os.Stat(newMI6); err == nil {
		if _, err := os.Stat(currentMI6); err == nil {
			vlog.Printf("swapping mi6-client: %s -> %s", newMI6, currentMI6)
			if err := atomicSwap(newMI6, currentMI6); err != nil {
				vlog.Printf("warning: failed to swap mi6-client: %v", err)
			}
		}
	}

	newHem := filepath.Join(stagedDir, "hem"+suffix)
	if _, err := os.Stat(currentHem); err == nil {
		vlog.Printf("swapping hem: %s -> %s", newHem, currentHem)
		if err := atomicSwap(newHem, currentHem); err != nil {
			return fmt.Errorf("swap hem: %w", err)
		}
	}

	newExe := filepath.Join(stagedDir, "moneypenny"+suffix)
	vlog.Printf("swapping moneypenny: %s -> %s", newExe, currentExe)
	if err := atomicSwap(newExe, currentExe); err != nil {
		return fmt.Errorf("swap binary: %w", err)
	}
	_ = os.RemoveAll(stagedDir)
	if beforeRestart != nil {
		beforeRestart()
	}
	vlog.Printf("re-execing with args: %v", args)
	return reExec(currentExe, args)
}
