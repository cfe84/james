//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// needsLogFileArg is true on Windows because Task Scheduler has no built-in
// stdout/stderr redirection. The binary must handle it via --log-file.
const needsLogFileArg = true

const taskNameUser = "JamesMoneypenny"
const taskNameSystem = "JamesMoneypennySystem"

func taskName(userLevel bool) string {
	if userLevel {
		return taskNameUser
	}
	return taskNameSystem
}

// cleanupLegacyTask removes the old single-name "JamesMoneypenny" task if it exists.
// Called during install to avoid orphaned tasks after the user/system split.
func cleanupLegacyTask() {
	// Check if legacy task exists.
	if err := exec.Command("schtasks", "/query", "/tn", "JamesMoneypenny", "/fo", "csv", "/nh").Run(); err != nil {
		return // not installed
	}
	_ = exec.Command("schtasks", "/end", "/tn", "JamesMoneypenny").Run()
	if err := exec.Command("schtasks", "/delete", "/tn", "JamesMoneypenny", "/f").Run(); err == nil {
		fmt.Printf("removed legacy scheduled task \"JamesMoneypenny\"\n")
	}
}

// Install creates a Windows Task Scheduler task that runs at logon.
func Install(cfg *Config) error {
	// Remove legacy single-name task if present.
	cleanupLegacyTask()

	// Ensure log directory exists.
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}

	// Build the argument string.
	args := cfg.BuildArgs()
	argStr := strings.Join(args, " ")

	tn := taskName(cfg.UserLevel)

	// schtasks /create for current user at logon.
	schtasksArgs := []string{
		"/create",
		"/tn", tn,
		"/tr", fmt.Sprintf(`"%s" %s`, cfg.BinaryPath, argStr),
		"/sc", "onlogon",
		"/rl", "limited",
		"/f", // force overwrite if exists
	}

	if !cfg.UserLevel {
		// System-level: run whether user is logged on or not.
		schtasksArgs = append(schtasksArgs, "/ru", "SYSTEM")
	}

	cmd := exec.Command("schtasks", schtasksArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}

	fmt.Printf("scheduled task %q created\n", tn)

	// Start it now.
	startCmd := exec.Command("schtasks", "/run", "/tn", tn)
	startCmd.Stdout = os.Stdout
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		fmt.Printf("warning: could not start task immediately: %v\n", err)
		fmt.Printf("the task will start at next logon\n")
	} else {
		fmt.Printf("service started\n")
	}

	return nil
}

// Uninstall stops and removes the scheduled task.
func Uninstall(userLevel bool) error {
	tn := taskName(userLevel)

	// Stop the task.
	stopCmd := exec.Command("schtasks", "/end", "/tn", tn)
	stopCmd.Stdout = os.Stdout
	stopCmd.Stderr = os.Stderr
	_ = stopCmd.Run() // ignore if not running

	// Delete the task.
	delCmd := exec.Command("schtasks", "/delete", "/tn", tn, "/f")
	delCmd.Stdout = os.Stdout
	delCmd.Stderr = os.Stderr
	if err := delCmd.Run(); err != nil {
		return fmt.Errorf("schtasks delete: %w", err)
	}

	fmt.Printf("scheduled task %q removed\n", tn)
	return nil
}

// Status returns whether the task is installed and running.
func Status(userLevel bool) (installed bool, running bool, err error) {
	tn := taskName(userLevel)

	out, err := exec.Command("schtasks", "/query", "/tn", tn, "/fo", "csv", "/nh").Output()
	if err != nil && userLevel {
		// Check legacy task name (before user/system split in v1.0.3).
		// Legacy tasks used a single name for both levels; report under user-level only.
		out, err = exec.Command("schtasks", "/query", "/tn", "JamesMoneypenny", "/fo", "csv", "/nh").Output()
	}
	if err != nil {
		return false, false, nil
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return false, false, nil
	}
	running = strings.Contains(line, "Running")
	return true, running, nil
}
