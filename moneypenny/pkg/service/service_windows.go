//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const taskName = "JamesMoneypenny"

// Install creates a Windows Task Scheduler task that runs at logon.
func Install(cfg *Config) error {
	// Ensure log directory exists.
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}

	// Build the argument string.
	args := cfg.BuildArgs()
	argStr := strings.Join(args, " ")

	// schtasks /create for current user at logon.
	schtasksArgs := []string{
		"/create",
		"/tn", taskName,
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

	fmt.Printf("scheduled task %q created\n", taskName)

	// Start it now.
	startCmd := exec.Command("schtasks", "/run", "/tn", taskName)
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
	// Stop the task.
	stopCmd := exec.Command("schtasks", "/end", "/tn", taskName)
	stopCmd.Stdout = os.Stdout
	stopCmd.Stderr = os.Stderr
	_ = stopCmd.Run() // ignore if not running

	// Delete the task.
	delCmd := exec.Command("schtasks", "/delete", "/tn", taskName, "/f")
	delCmd.Stdout = os.Stdout
	delCmd.Stderr = os.Stderr
	if err := delCmd.Run(); err != nil {
		return fmt.Errorf("schtasks delete: %w", err)
	}

	fmt.Printf("scheduled task %q removed\n", taskName)
	return nil
}

// Status returns whether the task is installed and running.
func Status(userLevel bool) (installed bool, running bool, err error) {
	out, err := exec.Command("schtasks", "/query", "/tn", taskName, "/fo", "csv", "/nh").Output()
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
