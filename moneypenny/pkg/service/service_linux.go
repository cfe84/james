//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// needsLogFileArg is false on Linux — systemd handles log redirection via
// StandardOutput/StandardError in the unit file.
const needsLogFileArg = false

const unitName = "moneypenny.service"

const unitTemplate = `[Unit]
Description=Moneypenny - James Agent Daemon
After=network.target

[Service]
Type=simple
ExecStart={{.Shell}} -l -c "exec {{.Command}}"
Restart=on-failure
RestartSec=5
StandardOutput=append:{{.LogFile}}
StandardError=append:{{.LogFile}}
{{- if .UserName}}
User={{.UserName}}
{{- end}}

[Install]
WantedBy={{.WantedBy}}
`

type unitData struct {
	Shell    string
	Command  string
	LogFile  string
	UserName string
	WantedBy string
}

func unitPath(userLevel bool) string {
	if userLevel {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "systemd", "user", unitName)
	}
	return filepath.Join("/etc", "systemd", "system", unitName)
}

// Install creates a systemd unit file and enables it.
func Install(cfg *Config) error {
	path := unitPath(cfg.UserLevel)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}

	// Ensure log directory exists.
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}

	// Detect user's login shell for environment loading.
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	// Build the full command string.
	cmdParts := []string{fmt.Sprintf("'%s'", cfg.BinaryPath)}
	for _, a := range cfg.BuildArgs() {
		if strings.Contains(a, " ") {
			cmdParts = append(cmdParts, fmt.Sprintf("'%s'", a))
		} else {
			cmdParts = append(cmdParts, a)
		}
	}

	data := unitData{
		Shell:    shell,
		Command:  strings.Join(cmdParts, " "),
		LogFile:  cfg.LogFile,
		WantedBy: "default.target",
	}

	if !cfg.UserLevel {
		data.WantedBy = "multi-user.target"
		if u := os.Getenv("USER"); u != "" {
			data.UserName = u
		}
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return fmt.Errorf("parse unit template: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create unit file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}

	fmt.Printf("wrote %s\n", path)

	// Enable and start.
	if cfg.UserLevel {
		run("systemctl", "--user", "daemon-reload")
		if err := run("systemctl", "--user", "enable", "--now", unitName); err != nil {
			return fmt.Errorf("systemctl enable: %w", err)
		}
	} else {
		run("sudo", "systemctl", "daemon-reload")
		if err := run("sudo", "systemctl", "enable", "--now", unitName); err != nil {
			return fmt.Errorf("systemctl enable: %w", err)
		}
	}

	fmt.Printf("service enabled and started\n")
	return nil
}

// Uninstall stops, disables, and removes the systemd unit.
func Uninstall(userLevel bool) error {
	path := unitPath(userLevel)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("service not installed (no unit at %s)", path)
	}

	if userLevel {
		_ = run("systemctl", "--user", "stop", unitName)
		_ = run("systemctl", "--user", "disable", unitName)
	} else {
		_ = run("sudo", "systemctl", "stop", unitName)
		_ = run("sudo", "systemctl", "disable", unitName)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove unit: %w", err)
	}

	if userLevel {
		run("systemctl", "--user", "daemon-reload")
	} else {
		run("sudo", "systemctl", "daemon-reload")
	}

	fmt.Printf("service uninstalled (removed %s)\n", path)
	return nil
}

// Status returns whether the service is installed and running.
func Status(userLevel bool) (installed bool, running bool, err error) {
	path := unitPath(userLevel)
	if _, err := os.Stat(path); err != nil {
		return false, false, nil
	}

	var out []byte
	if userLevel {
		out, err = exec.Command("systemctl", "--user", "is-active", unitName).Output()
	} else {
		out, err = exec.Command("systemctl", "is-active", unitName).Output()
	}
	running = strings.TrimSpace(string(out)) == "active"
	return true, running, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
