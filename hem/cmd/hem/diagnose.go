package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"james/hem/pkg/commands"
	"james/hem/pkg/hemclient"
	"james/hem/pkg/output"
	"james/hem/pkg/protocol"
	"james/hem/pkg/server"
	"james/hem/pkg/store"
)

// diagnoseCheck represents a single diagnostic check result for JSON output.
type diagnoseCheck struct {
	Name    string      `json:"name"`
	Status  string      `json:"status"` // "ok", "warn", "fail", "skip"
	Message string      `json:"message,omitempty"`
	Detail  interface{} `json:"detail,omitempty"`
}

// runDiagnose runs client-side and server-side diagnostics with streaming text output.
// It builds its own sender to avoid fatal errors on MI6 connect failure (local checks should still run).
func runDiagnose(mi6Addr string, outputFmt string) {
	isJSON := outputFmt == output.FormatJSON
	var checks []diagnoseCheck

	emit := func(icon, name, msg string, status string, detail interface{}) {
		checks = append(checks, diagnoseCheck{Name: name, Status: status, Message: msg, Detail: detail})
		if !isJSON {
			fmt.Printf("%-22s %s  %s\n", name, icon, msg)
		}
	}
	ok := func(name, msg string, detail ...interface{}) {
		var d interface{}
		if len(detail) > 0 {
			d = detail[0]
		}
		emit("\033[32m✓\033[0m", name, msg, "ok", d)
	}
	warn := func(name, msg string, detail ...interface{}) {
		var d interface{}
		if len(detail) > 0 {
			d = detail[0]
		}
		emit("\033[33m⚠\033[0m", name, msg, "warn", d)
	}
	fail := func(name, msg string, detail ...interface{}) {
		var d interface{}
		if len(detail) > 0 {
			d = detail[0]
		}
		emit("\033[31m✗\033[0m", name, msg, "fail", d)
	}

	if !isJSON {
		fmt.Printf("hem diagnose v%s\n\n", Version)
	}

	// === Phase 1: Local checks (no server needed) ===

	dataDir := defaultDataDir()

	// Check 1: Data directory.
	if info, err := os.Stat(dataDir); err != nil {
		fail("Data directory", fmt.Sprintf("missing: %s", dataDir))
	} else if !info.IsDir() {
		fail("Data directory", fmt.Sprintf("not a directory: %s", dataDir))
	} else {
		// Check writable by creating a temp file.
		f, err := os.CreateTemp(dataDir, "diagnose-*")
		if err != nil {
			fail("Data directory", fmt.Sprintf("not writable: %s", dataDir))
		} else {
			f.Close()
			os.Remove(f.Name())
			ok("Data directory", dataDir)
		}
	}

	// Check 2: SSH key pair.
	keyPath := filepath.Join(dataDir, "hem_ecdsa")
	pubKeyPath := keyPath + ".pub"
	if _, err := os.Stat(keyPath); err != nil {
		fail("SSH keys", fmt.Sprintf("private key missing: %s", keyPath))
	} else if _, err := os.Stat(pubKeyPath); err != nil {
		warn("SSH keys", fmt.Sprintf("public key missing: %s", pubKeyPath))
	} else {
		pubKey, err := loadOrCreatePublicKey(keyPath)
		if err != nil {
			fail("SSH keys", fmt.Sprintf("failed to load key: %v", err))
		} else {
			fp := ssh.FingerprintSHA256(pubKey)
			ok("SSH keys", fp)
		}
	}

	// Check 3: Database.
	dbPath := filepath.Join(dataDir, "hem.db")
	if _, err := os.Stat(dbPath); err != nil {
		fail("Database", fmt.Sprintf("missing: %s", dbPath))
	} else {
		st, err := store.New(dbPath)
		if err != nil {
			fail("Database", fmt.Sprintf("cannot open: %v", err))
		} else {
			// Quick integrity check: list moneypennies, sessions, projects.
			mps, errMP := st.ListMoneypennies()
			sessions, errSess := st.ListTrackedSessions("")
			projects, errProj := st.ListProjects("")
			if errMP != nil || errSess != nil || errProj != nil {
				warn("Database", fmt.Sprintf("open but queries failed: mp=%v sess=%v proj=%v", errMP, errSess, errProj))
			} else {
				ok("Database", fmt.Sprintf("ok — %d moneypennies, %d sessions, %d projects", len(mps), len(sessions), len(projects)))
			}
			st.Close()
		}
	}

	// === Phase 2: Server connection ===

	if !isJSON {
		fmt.Println()
	}

	connDesc := "unix://" + server.DefaultSocketPath()
	if mi6Addr != "" {
		connDesc = "mi6://" + mi6Addr
	}

	// Build sender for Phase 2 (non-fatal on MI6 connect failure).
	var sender hemclient.Sender
	var senderErr error
	if mi6Addr == "" {
		sender = &hemclient.SocketSender{SockPath: server.DefaultSocketPath()}
	} else {
		dataDir := defaultDataDir()
		keyPath := filepath.Join(dataDir, "hem_ecdsa")
		s := &hemclient.MI6Sender{Addr: mi6Addr, KeyPath: keyPath}
		if err := s.Connect(); err != nil {
			senderErr = fmt.Errorf("MI6 connect: %w", err)
		} else {
			sender = s
		}
	}

	if senderErr != nil {
		fail("Server connection", fmt.Sprintf("%s — %v", connDesc, senderErr))
		if isJSON {
			printDiagnoseJSON(checks)
		}
		return
	}

	start := time.Now()
	req := &protocol.Request{Verb: "diagnose"}
	resp, err := sender.Send(req)
	serverLatency := time.Since(start)

	if err != nil {
		fail("Server connection", fmt.Sprintf("%s — %v", connDesc, err))
		// Can't continue without server.
		if isJSON {
			printDiagnoseJSON(checks)
		}
		return
	}
	if resp.Status == protocol.StatusError {
		fail("Server connection", fmt.Sprintf("%s — %s", connDesc, resp.Message))
		if isJSON {
			printDiagnoseJSON(checks)
		}
		return
	}
	ok("Server connection", fmt.Sprintf("%s (%dms)", connDesc, serverLatency.Milliseconds()))

	// === Phase 3: Unpack server diagnosis ===

	var diag commands.DiagnoseResult
	if err := json.Unmarshal(resp.Data, &diag); err != nil {
		fail("Server response", fmt.Sprintf("invalid response: %v", err))
		if isJSON {
			printDiagnoseJSON(checks)
		}
		return
	}

	// Server version check.
	if diag.ServerVersion != Version {
		warn("Server version", fmt.Sprintf("server=%s client=%s (mismatch)", diag.ServerVersion, Version))
	}

	// MI6 control.
	if diag.MI6Control != "" {
		ok("MI6 control", diag.MI6Control)
	} else {
		emit("\033[90m—\033[0m", "MI6 control", "not configured", "skip", nil)
	}

	// Moneypennies.
	if !isJSON {
		fmt.Println()
	}
	if len(diag.Moneypennies) == 0 {
		warn("Moneypennies", "none registered")
	} else {
		for _, mp := range diag.Moneypennies {
			label := fmt.Sprintf("  %s (%s)", mp.Name, mp.Transport)
			if !mp.Enabled {
				emit("\033[90m—\033[0m", label, "disabled", "skip", nil)
				continue
			}
			if !mp.Reachable {
				msg := mp.Error
				if mp.InCooldown {
					msg += fmt.Sprintf(" (cooldown %s)", mp.CooldownRemaining)
				}
				fail(label, msg)
				continue
			}
			// Reachable.
			msg := fmt.Sprintf("v%s (%dms)", mp.Version, mp.LatencyMs)
			if mp.VersionMismatch {
				warn(label, msg+" ⚠ version mismatch")
			} else {
				ok(label, msg)
			}
			// Agents.
			if mp.AgentCheckSkipped != "" {
				emit("\033[90m—\033[0m", "    agents", mp.AgentCheckSkipped, "skip", nil)
			} else if len(mp.Agents) > 0 {
				for _, a := range mp.Agents {
					agentLabel := fmt.Sprintf("    %s", a.Name)
					if a.Found {
						ok(agentLabel, a.Path)
					} else {
						warn(agentLabel, "not found in PATH")
					}
				}
			}
		}
	}

	// Sessions.
	if !isJSON {
		fmt.Println()
	}
	sessionParts := []string{}
	total := 0
	for status, count := range diag.SessionCounts {
		sessionParts = append(sessionParts, fmt.Sprintf("%d %s", count, status))
		total += count
	}
	if total == 0 {
		emit("\033[90m—\033[0m", "Sessions", "none", "skip", nil)
	} else {
		ok("Sessions", strings.Join(sessionParts, ", "))
	}

	// Cache.
	if diag.CacheAgeSeconds < 0 {
		warn("Cache", "never refreshed")
	} else {
		age := time.Duration(diag.CacheAgeSeconds * float64(time.Second))
		msg := fmt.Sprintf("last refresh %s ago", age.Round(time.Second))
		if diag.CacheRefreshing {
			msg += " (refreshing)"
		}
		if diag.CacheAgeSeconds > 600 { // >10 min
			warn("Cache", msg+" — stale")
		} else {
			ok("Cache", msg)
		}
	}

	if isJSON {
		printDiagnoseJSON(checks)
	}
}

// printDiagnoseJSON outputs all diagnose checks as a JSON array.
func printDiagnoseJSON(checks []diagnoseCheck) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(checks)
}
