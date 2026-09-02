//go:build windows

package updater

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type windowsUpdatePlan struct {
	ParentPID  int      `json:"parent_pid"`
	CurrentExe string   `json:"current_exe"`
	CurrentMI6 string   `json:"current_mi6"`
	StagedDir  string   `json:"staged_dir"`
	Args       []string `json:"args"`
}

func installStagedUpdate(stagedDir, currentExe, currentMI6 string, args []string, beforeRestart func(), vlog *log.Logger) error {
	helper := filepath.Join(stagedDir, "moneypenny-update-helper.exe")
	if _, err := os.Stat(helper); err != nil {
		return fmt.Errorf("staged update helper not found: %w", err)
	}
	planPath := filepath.Join(stagedDir, "update-plan.json")
	plan, err := json.Marshal(windowsUpdatePlan{
		ParentPID:  os.Getpid(),
		CurrentExe: currentExe,
		CurrentMI6: currentMI6,
		StagedDir:  stagedDir,
		Args:       args[1:],
	})
	if err != nil {
		return fmt.Errorf("encode update plan: %w", err)
	}
	if err := os.WriteFile(planPath, plan, 0600); err != nil {
		return fmt.Errorf("write update plan: %w", err)
	}
	cmd := exec.Command(helper, "--plan", planPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start update helper: %w", err)
	}
	vlog.Printf("started update helper for PID %d", os.Getpid())
	if beforeRestart != nil {
		beforeRestart()
	}
	os.Exit(0)
	return nil
}
