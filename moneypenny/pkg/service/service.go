// Package service installs/uninstalls moneypenny as a system service
// using the appropriate mechanism for each OS.
package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the service configuration gathered from the wizard.
type Config struct {
	BinaryPath     string // absolute path to the moneypenny binary
	MI6Address     string // optional MI6 address (host/session_id)
	AutoUpdate     bool
	UpdateInterval string // e.g. "1h", "30m"
	DataDir        string
	LogFile string // path to log file
	Local   bool   // run in local FIFO mode
	Verbose         bool
	UserLevel       bool // true = user-level service, false = system-level
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
