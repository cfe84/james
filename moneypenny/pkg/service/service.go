// Package service installs/uninstalls moneypenny as a system service
// using the appropriate mechanism for each OS.
package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the service configuration gathered from the wizard.
type Config struct {
	BinaryPath           string // absolute path to the moneypenny binary
	MI6Address           string // optional MI6 address (host/session_id)
	MI6ServerFingerprint string // required SHA256 fingerprint of the MI6 server
	AutoUpdate           bool
	UpdateInterval       string // e.g. "1h", "30m"
	DataDir              string
	LogFile              string // path to log file
	Local                bool   // run in local FIFO mode
	Verbose              bool
	UserLevel            bool // true = user-level service, false = system-level
}

// DefaultLogFile returns the default log file path in the data dir.
func DefaultLogFile(dataDir string) string {
	return filepath.Join(dataDir, "moneypenny.log")
}

// BuildArgs constructs the moneypenny CLI arguments from the config.
func (c *Config) BuildArgs() []string {
	var args []string
	if c.MI6Address != "" {
		args = append(args, "--mi6", c.MI6Address)
		args = append(args, "--mi6-server-fingerprint", c.MI6ServerFingerprint)
	} else if c.Local {
		args = append(args, "--local")
	}
	if c.DataDir != "" {
		args = append(args, "--data-dir", c.DataDir)
	}
	if c.AutoUpdate {
		args = append(args, "--auto-update")
		if c.UpdateInterval != "" {
			args = append(args, "--update-interval", c.UpdateInterval)
		}
	}
	if c.Verbose {
		args = append(args, "-v")
	}
	if c.LogFile != "" && needsLogFileArg {
		args = append(args, "--log-file", c.LogFile)
	}
	return args
}

// ResolveBinaryPath returns the absolute path of the currently running binary.
func ResolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("get executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	return exe, nil
}

// windowsTaskDefinition returns a Task Scheduler definition that restarts the
// task on a nonzero daemon exit. The launcher propagates that exit code.
func windowsTaskDefinition(cfg *Config, launcherPath, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("Windows task user is required")
	}

	escape := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	logonType := "InteractiveToken"
	runLevel := "LeastPrivilege"
	trigger := "<LogonTrigger><Enabled>true</Enabled></LogonTrigger>"
	if !cfg.UserLevel {
		userID = "S-1-5-18"
		logonType = "ServiceAccount"
		runLevel = "HighestAvailable"
		trigger = "<BootTrigger><Enabled>true</Enabled></BootTrigger>"
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    %s
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>%s</LogonType>
      <RunLevel>%s</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>wscript.exe</Command>
      <Arguments>//B "%s"</Arguments>
    </Exec>
  </Actions>
</Task>
`, trigger, escape(userID), logonType, runLevel, escape(launcherPath)), nil
}
