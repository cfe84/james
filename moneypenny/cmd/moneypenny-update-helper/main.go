//go:build windows

// moneypenny-update-helper applies a staged update after the running daemon
// exits, because Windows locks executable files while they are in use.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type updatePlan struct {
	ParentPID  int      `json:"parent_pid"`
	CurrentExe string   `json:"current_exe"`
	CurrentMI6 string   `json:"current_mi6"`
	CurrentHem string   `json:"current_hem"`
	StagedDir  string   `json:"staged_dir"`
	Args       []string `json:"args"`
}

func main() {
	planPath := flag.String("plan", "", "path to the update plan")
	flag.Parse()
	if *planPath == "" {
		log.Fatal("--plan is required")
	}
	if err := apply(*planPath); err != nil {
		log.Fatal(err)
	}
}

func apply(planPath string) error {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read update plan: %w", err)
	}
	var plan updatePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("decode update plan: %w", err)
	}
	if plan.ParentPID <= 0 || plan.CurrentExe == "" || plan.StagedDir == "" {
		return fmt.Errorf("invalid update plan")
	}
	if err := waitForExit(plan.ParentPID, 30*time.Second); err != nil {
		return err
	}

	newExe := filepath.Join(plan.StagedDir, "moneypenny.exe")
	if err := replaceFile(newExe, plan.CurrentExe); err != nil {
		return fmt.Errorf("replace moneypenny executable: %w", err)
	}
	newMI6 := filepath.Join(plan.StagedDir, "mi6-client.exe")
	if _, err := os.Stat(newMI6); err == nil {
		if _, err := os.Stat(plan.CurrentMI6); err == nil {
			if err := replaceFile(newMI6, plan.CurrentMI6); err != nil {
				log.Printf("warning: replace mi6-client: %v", err)
			}
			newHem := filepath.Join(plan.StagedDir, "hem.exe")
			if _, err := os.Stat(newHem); err == nil {
				if _, err := os.Stat(plan.CurrentHem); err == nil {
					if err := replaceFile(newHem, plan.CurrentHem); err != nil {
						log.Printf("warning: replace hem: %v", err)
					}
				}
			}
		}
	}

	cmd := exec.Command(plan.CurrentExe, plan.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updated moneypenny: %w", err)
	}
	log.Printf("started updated moneypenny")
	_ = os.RemoveAll(plan.StagedDir)
	return nil
}

func waitForExit(pid int, timeout time.Duration) error {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// The process may have already exited before the helper opens it.
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return fmt.Errorf("open parent process: %w", err)
	}
	defer windows.CloseHandle(process)
	status, err := windows.WaitForSingleObject(process, uint32(timeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("wait for parent process: %w", err)
	}
	if status != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("timed out waiting for parent process %d to exit", pid)
	}
	return nil
}

func replaceFile(source, target string) error {
	backup := target + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("backup existing file: %w", err)
	}
	if err := os.Rename(source, target); err != nil {
		if err := copyFile(source, target); err != nil {
			_ = os.Rename(backup, target)
			return fmt.Errorf("install replacement: %w", err)
		}
	}
	_ = os.Remove(backup)
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := out.ReadFrom(in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
