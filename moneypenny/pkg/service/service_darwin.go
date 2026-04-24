//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// needsLogFileArg is false on macOS — launchd handles log redirection via
// StandardOutPath/StandardErrorPath in the plist.
const needsLogFileArg = false

const plistLabel = "net.cingen.james.moneypenny"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
{{- range .Args}}
        <string>{{.}}</string>
{{- end}}
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogFile}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogFile}}</string>
{{- if .UserName}}
    <key>UserName</key>
    <string>{{.UserName}}</string>
{{- end}}
</dict>
</plist>
`

type plistData struct {
	Label      string
	BinaryPath string
	Args       []string
	LogFile    string
	UserName   string
}

func plistPath(userLevel bool) string {
	if userLevel {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
	}
	return filepath.Join("/Library", "LaunchDaemons", plistLabel+".plist")
}

// Install creates a launchd plist and loads it.
func Install(cfg *Config) error {
	path := plistPath(cfg.UserLevel)

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create plist directory: %w", err)
	}

	// Ensure log directory exists.
	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}

	data := plistData{
		Label:      plistLabel,
		BinaryPath: cfg.BinaryPath,
		Args:       cfg.BuildArgs(),
		LogFile:    cfg.LogFile,
	}

	// For system-level daemons, run as the current user.
	if !cfg.UserLevel {
		if u := os.Getenv("USER"); u != "" {
			data.UserName = u
		}
	}

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parse plist template: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create plist file: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	fmt.Printf("wrote %s\n", path)

	// Load the service.
	var cmd *exec.Cmd
	if cfg.UserLevel {
		cmd = exec.Command("launchctl", "load", path)
	} else {
		cmd = exec.Command("sudo", "launchctl", "load", path)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}

	fmt.Printf("service loaded and started\n")
	return nil
}

// Uninstall stops and removes the launchd plist.
func Uninstall(userLevel bool) error {
	path := plistPath(userLevel)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("service not installed (no plist at %s)", path)
	}

	// Unload.
	var cmd *exec.Cmd
	if userLevel {
		cmd = exec.Command("launchctl", "unload", path)
	} else {
		cmd = exec.Command("sudo", "launchctl", "unload", path)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run() // ignore error if not loaded

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove plist: %w", err)
	}

	fmt.Printf("service uninstalled (removed %s)\n", path)
	return nil
}

// Status returns whether the service is installed and running.
func Status(userLevel bool) (installed bool, running bool, err error) {
	path := plistPath(userLevel)
	if _, err := os.Stat(path); err != nil {
		return false, false, nil
	}

	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return true, false, nil
	}
	running = strings.Contains(string(out), plistLabel)
	return true, running, nil
}
