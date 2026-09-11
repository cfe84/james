//go:build windows

package service

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// needsLogFileArg is true on Windows because Task Scheduler has no built-in
// stdout/stderr redirection. The binary must handle it via --log-file.
const needsLogFileArg = true

const taskNameUser = "JamesMoneypenny"
const taskNameSystem = "JamesMoneypennySystem"
const taskWrapperName = "moneypenny-service.cmd"
const taskLauncherName = "moneypenny-service.vbs"
const taskDefinitionName = "moneypenny-service.xml"
const crashLogDirectoryName = "crash-logs"

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

	launcherPath, err := writeTaskLauncher(cfg)
	if err != nil {
		return err
	}

	tn := taskName(cfg.UserLevel)
	definitionPath, err := writeTaskDefinition(cfg, launcherPath)
	if err != nil {
		return err
	}
	defer os.Remove(definitionPath)

	// The task definition runs the managed wrapper through WScript so the
	// daemon console remains hidden, has no execution time limit, and restarts
	// after an unexpected nonzero exit.
	schtasksArgs := []string{
		"/create",
		"/tn", tn,
		"/xml", definitionPath,
		"/f", // force overwrite if exists
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

func writeTaskWrapper(cfg *Config) (string, error) {
	if cfg.DataDir == "" {
		return "", fmt.Errorf("data directory is required for Windows service installation")
	}
	if cfg.LogFile == "" {
		return "", fmt.Errorf("log file is required for Windows service installation")
	}
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}

	command := make([]string, 0, 1+len(cfg.BuildArgs()))
	command = append(command, cfg.BinaryPath)
	command = append(command, cfg.BuildArgs()...)
	for _, arg := range command {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return "", fmt.Errorf("service command argument contains an invalid control character")
		}
	}
	for _, path := range []string{cfg.DataDir, cfg.LogFile} {
		if strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("service log path contains an invalid control character")
		}
	}

	var script strings.Builder
	script.WriteString("@echo off\r\n")
	for i, arg := range command {
		if i > 0 {
			script.WriteByte(' ')
		}
		script.WriteString(quoteBatchArg(arg))
	}
	script.WriteString("\r\nset \"exitCode=%ERRORLEVEL%\"\r\n")
	script.WriteString("if \"%exitCode%\"==\"0\" exit /b 0\r\n")
	script.WriteString("if exist ")
	script.WriteString(quoteBatchArg(cfg.LogFile))
	script.WriteString(" (\r\n")
	script.WriteString("  if not exist ")
	script.WriteString(quoteBatchArg(filepath.Join(cfg.DataDir, crashLogDirectoryName)))
	script.WriteString(" mkdir ")
	script.WriteString(quoteBatchArg(filepath.Join(cfg.DataDir, crashLogDirectoryName)))
	script.WriteString("\r\n")
	script.WriteString("  powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command ")
	script.WriteString(quoteBatchArg(crashLogPowerShellCommand))
	script.WriteString(" ")
	script.WriteString(quoteBatchArg(cfg.LogFile))
	script.WriteString(" ")
	script.WriteString(quoteBatchArg(filepath.Join(cfg.DataDir, crashLogDirectoryName)))
	script.WriteString(" \"%exitCode%\"\r\n)\r\nexit /b %exitCode%\r\n")

	path := filepath.Join(cfg.DataDir, taskWrapperName)
	if err := os.WriteFile(path, []byte(script.String()), 0600); err != nil {
		return "", fmt.Errorf("write task wrapper: %w", err)
	}
	return path, nil
}

const crashLogPowerShellCommand = `$logPath, $crashDir, $exitCode = $args; ` +
	`$timestamp = Get-Date -Format 'yyyyMMdd-HHmmss'; ` +
	`Copy-Item -LiteralPath $logPath -Destination (Join-Path $crashDir ("moneypenny-crash-$timestamp-exit-$exitCode.log")) -Force; ` +
	`Get-ChildItem -LiteralPath $crashDir -Filter 'moneypenny-crash-*.log' | Sort-Object LastWriteTimeUtc -Descending | Select-Object -Skip 10 | Remove-Item -Force`

func writeTaskLauncher(cfg *Config) (string, error) {
	wrapperPath, err := writeTaskWrapper(cfg)
	if err != nil {
		return "", err
	}

	// WScript's window style 0 hides the cmd.exe console; waitOnReturn keeps
	// Task Scheduler tracking the daemon process rather than the launcher.
	// Propagating the child exit code lets Task Scheduler distinguish a clean
	// update/shutdown from a crash for RestartOnFailure.
	script := fmt.Sprintf(
		`Set shell = CreateObject("WScript.Shell")`+"\r\n"+
			`exitCode = shell.Run("cmd.exe /d /s /c """"%s""""", 0, True)`+"\r\n"+
			`WScript.Quit exitCode`+"\r\n",
		strings.ReplaceAll(wrapperPath, `"`, `""`),
	)
	path := filepath.Join(cfg.DataDir, taskLauncherName)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		return "", fmt.Errorf("write task launcher: %w", err)
	}
	return path, nil
}

func writeTaskDefinition(cfg *Config, launcherPath string) (string, error) {
	userID := os.Getenv("USERNAME")
	var err error
	if cfg.UserLevel {
		userID, err = currentWindowsUserSID()
		if err != nil {
			return "", err
		}
	}
	definition, err := windowsTaskDefinition(cfg, launcherPath, userID)
	if err != nil {
		return "", err
	}

	path := filepath.Join(cfg.DataDir, taskDefinitionName)
	if err := os.WriteFile(path, utf16LEWithBOM(definition), 0600); err != nil {
		return "", fmt.Errorf("write task definition: %w", err)
	}
	return path, nil
}

// currentWindowsUserSID returns the SID expected by Task Scheduler when an
// interactive-token task is imported from XML. USERNAME is not sufficient:
// it omits the domain and can be rejected with ERROR_ACCESS_DENIED.
func currentWindowsUserSID() (string, error) {
	output, err := exec.Command("whoami", "/user", "/fo", "csv", "/nh").Output()
	if err != nil {
		return "", fmt.Errorf("resolve Windows user SID: %w", err)
	}
	record, err := csv.NewReader(strings.NewReader(string(output))).Read()
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("parse Windows user SID: %w", err)
	}
	if len(record) < 2 || !strings.HasPrefix(record[1], "S-1-") {
		return "", fmt.Errorf("resolve Windows user SID: whoami returned an invalid SID")
	}
	return record[1], nil
}

// schtasks is inconsistent about UTF-8 task-definition files. UTF-16LE with a
// BOM is the native Task Scheduler XML encoding and avoids code-page switching.
func utf16LEWithBOM(s string) []byte {
	runes := utf16.Encode([]rune(s))
	data := make([]byte, 2+len(runes)*2)
	binary.LittleEndian.PutUint16(data, 0xFEFF)
	for i, r := range runes {
		binary.LittleEndian.PutUint16(data[2+i*2:], r)
	}
	return data
}

func quoteBatchArg(arg string) string {
	// Doubling '%' prevents batch-variable expansion. Paths and supported
	// service arguments cannot contain literal double quotes.
	return `"` + strings.ReplaceAll(arg, "%", "%%") + `"`
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
