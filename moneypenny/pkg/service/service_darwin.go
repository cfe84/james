//go:build darwin

package service

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
{{- range .Arguments}}
        <string>{{xml .}}</string>
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
	Label     string
	Arguments []string
	LogFile   string
	UserName  string
}

func plistPath(userLevel bool) string {
	if userLevel {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "LaunchAgents", plistLabel+".plist")
	}
	return filepath.Join("/Library", "LaunchDaemons", plistLabel+".plist")
}

func launchdDomain(userLevel bool) string {
	if !userLevel {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchdTarget(userLevel bool) string {
	return launchdDomain(userLevel) + "/" + plistLabel
}

func runLaunchctl(args ...string) ([]byte, error) {
	return exec.Command("launchctl", args...).CombinedOutput()
}

func launchdServiceAbsent(err error, output []byte) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(string(output))
	return strings.Contains(message, "no such process") ||
		strings.Contains(message, "could not find service")
}

// Install creates a launchd plist and loads it.
func Install(cfg *Config) error {
	if cfg.UserLevel && os.Geteuid() == 0 {
		return fmt.Errorf("user-level launchd services must be installed without sudo")
	}

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
		Label:     plistLabel,
		Arguments: append([]string{cfg.BinaryPath}, cfg.BuildArgs()...),
		LogFile:   cfg.LogFile,
	}

	// For system-level daemons, run as the current user.
	if !cfg.UserLevel {
		if u := os.Getenv("USER"); u != "" {
			data.UserName = u
		}
	}

	tmpl, err := template.New("plist").Funcs(template.FuncMap{
		"xml": func(value string) string {
			var escaped bytes.Buffer
			_ = xml.EscapeText(&escaped, []byte(value))
			return escaped.String()
		},
	}).Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parse plist template: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create plist file: %w", err)
	}

	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write plist: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close plist file: %w", err)
	}

	fmt.Printf("wrote %s\n", path)

	if output, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
		return fmt.Errorf("validate plist: %w: %s", err, strings.TrimSpace(string(output)))
	}

	// Use the explicit launchd domain APIs. The legacy `load` command can print
	// "Load failed" while still returning success on current macOS releases.
	args := []string{"bootstrap", launchdDomain(cfg.UserLevel), path}
	target := launchdTarget(cfg.UserLevel)
	var output []byte
	if cfg.UserLevel {
		output, err = runLaunchctl("enable", target)
	} else {
		output, err = exec.Command("sudo", "launchctl", "enable", target).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("launchctl enable: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if cfg.UserLevel {
		output, err = runLaunchctl(args...)
	} else {
		output, err = exec.Command("sudo", append([]string{"launchctl"}, args...)...).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := verifyLoaded(cfg.UserLevel); err != nil {
		return err
	}

	fmt.Printf("service bootstrapped and started\n")
	return nil
}

func verifyLoaded(userLevel bool) error {
	var output []byte
	var err error
	if userLevel {
		output, err = runLaunchctl("print", launchdTarget(userLevel))
	} else {
		output, err = exec.Command("sudo", "launchctl", "print", launchdTarget(userLevel)).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("service bootstrap did not register %s: %w: %s",
			launchdTarget(userLevel), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Uninstall stops and removes the launchd plist.
func Uninstall(userLevel bool) error {
	path := plistPath(userLevel)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("service not installed (no plist at %s)", path)
	}

	// Boot out the explicit domain target. It is safe to continue when the
	// service was already absent, but removal errors must remain visible.
	args := []string{"bootout", launchdTarget(userLevel)}
	var output []byte
	var err error
	if userLevel {
		output, err = runLaunchctl(args...)
	} else {
		output, err = exec.Command("sudo", append([]string{"launchctl"}, args...)...).CombinedOutput()
	}
	if err != nil && !launchdServiceAbsent(err, output) {
		return fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(output)))
	}

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

	if userLevel {
		_, err = runLaunchctl("print", launchdTarget(userLevel))
	} else {
		err = exec.Command("sudo", "launchctl", "print", launchdTarget(userLevel)).Run()
	}
	running = err == nil
	return true, running, nil
}
