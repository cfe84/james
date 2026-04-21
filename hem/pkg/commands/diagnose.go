package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

// DiagnoseResult is the structured result from the server-side diagnose command.
type DiagnoseResult struct {
	ServerVersion   string         `json:"server_version"`
	MI6Control      string         `json:"mi6_control,omitempty"` // configured MI6 control address, or ""
	Moneypennies    []MPDiagnosis  `json:"moneypennies"`
	SessionCounts   map[string]int `json:"session_counts"`    // hem_status → count
	CacheAgeSeconds float64        `json:"cache_age_seconds"` // seconds since last cache refresh (-1 = never)
	CacheRefreshing bool           `json:"cache_refreshing"`
}

// MPDiagnosis holds diagnostic info for a single moneypenny.
type MPDiagnosis struct {
	Name              string      `json:"name"`
	Transport         string      `json:"transport"`
	Address           string      `json:"address"`
	Enabled           bool        `json:"enabled"`
	IsDefault         bool        `json:"is_default"`
	Reachable         bool        `json:"reachable"`
	Error             string      `json:"error,omitempty"`
	Version           string      `json:"version,omitempty"`
	LatencyMs         int64       `json:"latency_ms,omitempty"`
	InCooldown        bool        `json:"in_cooldown"`
	CooldownRemaining string      `json:"cooldown_remaining,omitempty"`
	VersionMismatch   bool        `json:"version_mismatch,omitempty"`
	Agents            []AgentInfo `json:"agents,omitempty"`
	AgentCheckSkipped string      `json:"agent_check_skipped,omitempty"`
}

// AgentInfo describes an agent binary on a moneypenny host.
type AgentInfo struct {
	Name  string `json:"name"`
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
}

// Diagnose runs server-side diagnostics: pings all moneypennies, checks agents,
// reports cache state and session counts.
func (e *Executor) Diagnose(args []string) *protocol.Response {
	mps, err := e.store.ListMoneypennies()
	if err != nil {
		return protocol.ErrResponse(fmt.Sprintf("failed to list moneypennies: %v", err))
	}

	// Ping all moneypennies in parallel and collect version + latency.
	type pingResult struct {
		name    string
		version string
		latency time.Duration
		err     error
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()

	pingCh := make(chan pingResult, len(mps))
	for _, mp := range mps {
		go func(mp *store.Moneypenny) {
			start := time.Now()
			resp, err := e.sendCommand(pingCtx, mp, "get_version", nil)
			elapsed := time.Since(start)
			if err != nil {
				pingCh <- pingResult{name: mp.Name, err: err}
				return
			}
			var vd struct {
				Version string `json:"version"`
			}
			json.Unmarshal(resp.Data, &vd)
			pingCh <- pingResult{name: mp.Name, version: vd.Version, latency: elapsed}
		}(mp)
	}

	// Collect ping results.
	pingResults := make(map[string]pingResult, len(mps))
	for range mps {
		r := <-pingCh
		pingResults[r.name] = r
	}

	// For reachable moneypennies, check agents (with version gating).
	// Use a separate context so ping latency doesn't eat into the agent check budget.
	agentCtx, agentCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer agentCancel()

	type agentResult struct {
		name    string
		agents  []AgentInfo
		skipped string
	}
	agentCh := make(chan agentResult, len(mps))
	agentCount := 0
	for _, mp := range mps {
		pr := pingResults[mp.Name]
		if pr.err != nil {
			continue
		}
		// Version gate: check_agents was added in 1.0.0, skip if older.
		if pr.version != "" && versionLessThan(pr.version, "1.0.0") {
			agentCh <- agentResult{name: mp.Name, skipped: fmt.Sprintf("moneypenny too old (%s < 1.0.0)", pr.version)}
			agentCount++
			continue
		}
		agentCount++
		go func(mp *store.Moneypenny) {
			resp, err := e.sendCommand(agentCtx, mp, "check_agents", nil)
			if err != nil {
				errStr := err.Error()
				if strings.Contains(errStr, "unknown method") {
					agentCh <- agentResult{name: mp.Name, skipped: "command not supported (moneypenny too old)"}
				} else {
					agentCh <- agentResult{name: mp.Name, skipped: fmt.Sprintf("check failed: %v", err)}
				}
				return
			}
			var car struct {
				Agents []AgentInfo `json:"agents"`
			}
			if json.Unmarshal(resp.Data, &car) != nil {
				agentCh <- agentResult{name: mp.Name, skipped: "invalid response"}
				return
			}
			agentCh <- agentResult{name: mp.Name, agents: car.Agents}
		}(mp)
	}

	agentResults := make(map[string]agentResult, agentCount)
	for range agentCount {
		r := <-agentCh
		agentResults[r.name] = r
	}

	// Build per-moneypenny diagnosis.
	diagnoses := make([]MPDiagnosis, 0, len(mps))
	for _, mp := range mps {
		d := MPDiagnosis{
			Name:      mp.Name,
			Transport: mp.TransportType,
			Address:   moneypennyAddress(mp),
			Enabled:   mp.Enabled,
			IsDefault: mp.IsDefault,
		}

		// Cooldown status.
		if cu := e.clientManager.GetCooldownUntil(mp.Name); !cu.IsZero() {
			d.InCooldown = true
			d.CooldownRemaining = fmt.Sprintf("%.0fs", time.Until(cu).Seconds())
		}

		// Ping result.
		pr := pingResults[mp.Name]
		if pr.err != nil {
			d.Reachable = false
			d.Error = pr.err.Error()
		} else {
			d.Reachable = true
			d.Version = pr.version
			d.LatencyMs = pr.latency.Milliseconds()
			if pr.version != "" && pr.version != e.Version {
				d.VersionMismatch = true
			}
		}

		// Agent check.
		if ar, ok := agentResults[mp.Name]; ok {
			if ar.skipped != "" {
				d.AgentCheckSkipped = ar.skipped
			} else {
				d.Agents = ar.agents
			}
		}

		diagnoses = append(diagnoses, d)
	}

	// Session counts by hem_status.
	sessionCounts := make(map[string]int)
	sessions, _ := e.store.ListTrackedSessions("")
	for _, s := range sessions {
		status := s.HemStatus
		if status == "" {
			status = "active"
		}
		sessionCounts[status]++
	}

	// Cache state.
	cacheAge := float64(-1)
	cacheTime := e.cacheManager.GetCacheTime()
	if !cacheTime.IsZero() {
		cacheAge = time.Since(cacheTime).Seconds()
	}

	result := DiagnoseResult{
		ServerVersion:   e.Version,
		MI6Control:      e.MI6Control,
		Moneypennies:    diagnoses,
		SessionCounts:   sessionCounts,
		CacheAgeSeconds: cacheAge,
		CacheRefreshing: e.cacheManager.IsRefreshing(),
	}

	return protocol.OKResponse(result)
}
