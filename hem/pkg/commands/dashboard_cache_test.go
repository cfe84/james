package commands

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

func dashboardTable(t *testing.T, resp *protocol.Response) map[string][]string {
	t.Helper()
	if resp.Status != "ok" {
		t.Fatalf("command failed: %s", resp.Message)
	}
	var table TableResult
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		t.Fatal(err)
	}
	rows := make(map[string][]string)
	for _, row := range table.Rows {
		rows[row[0]] = row
	}
	return rows
}

func waitForMPRefresh(t *testing.T, e *Executor) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for e.cacheManager.IsRefreshing() {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDashboardUnavailablePreservesCachedMetadata(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mac", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	cached := make(map[string]mpSessionInfo)
	for _, id := range []string{"unreviewed", "reviewed", "working", "child"} {
		if err := e.store.TrackSession(id, "mac"); err != nil {
			t.Fatal(err)
		}
		cached[id] = mpSessionInfo{
			SessionID: id, Name: "Name " + id, Agent: "copilot", Status: "idle",
			CreatedAt: "2026-09-01T12:00:00Z", LastAccessed: "2026-09-09T12:00:00Z",
		}
	}
	working := cached["working"]
	working.Status = "working"
	cached["working"] = working
	if err := e.store.SetSessionReviewed("reviewed", true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSessionParent("child", "working"); err != nil {
		t.Fatal(err)
	}
	e.cacheManager.UpdateMP("mac", cached)
	e.cacheManager.SetRefreshing(true) // Keep these rendering assertions network-independent.
	e.markMPUnavailable("mac")
	e.invalidateMPCache("mac")

	rows := dashboardTable(t, e.Dashboard([]string{"--show-subs"}))
	if len(rows) != len(cached) {
		t.Fatalf("got %d rows, want %d", len(rows), len(cached))
	}
	for id, info := range cached {
		row := rows[id]
		if strings.TrimPrefix(row[1], "↳ ") != info.Name || row[8] != info.Agent ||
			row[5] != info.CreatedAt || row[6] != info.LastAccessed {
			t.Errorf("lost metadata for %s: %v", id, row)
		}
		if !strings.HasPrefix(row[3], "offline (active)") ||
			strings.Contains(row[3], "ready") || strings.Contains(row[3], "working") {
			t.Errorf("unavailable session has live status: %v", row)
		}
	}
	listed := dashboardTable(t, e.ListSessions(nil))
	if len(listed) != 3 {
		t.Fatalf("list returned %d rows, want 3 parents", len(listed))
	}
	for id, row := range listed {
		if row[1] != cached[id].Name || !strings.HasPrefix(row[2], "offline") {
			t.Errorf("session list lost fallback: %v", row)
		}
	}
	if got := e.cacheManager.GetSnapshot()["mac"]["working"]; got != working {
		t.Fatalf("offline rendering mutated last-known cache: %#v", got)
	}
}

func TestDashboardMissingMetadataStatuses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		hasCache    bool
		unavailable bool
		want        string
	}{
		{"cold offline", false, true, "offline"},
		{"missing offline", true, true, "offline"},
		{"missing online", true, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHierarchyExecutor(t)
			if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mac", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			if err := e.store.TrackSession("parent", "mac"); err != nil {
				t.Fatal(err)
			}
			if err := e.store.TrackSubSession("child", "mac", "parent"); err != nil {
				t.Fatal(err)
			}
			if tc.hasCache {
				e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{})
				e.cacheManager.SetRefreshing(true)
			}
			if tc.unavailable {
				e.markMPUnavailable("mac")
			}
			rows := dashboardTable(t, e.Dashboard([]string{"--show-subs"}))
			if !tc.hasCache {
				waitForMPRefresh(t, e)
			}
			for _, id := range []string{"parent", "child"} {
				if len(rows[id]) == 0 || !strings.HasPrefix(rows[id][3], tc.want+" (active)") {
					t.Fatalf("%s: got %v, want %s", id, rows[id], tc.want)
				}
			}
		})
	}
}

func TestMPRefreshFailureAndRecovery(t *testing.T) {
	for _, name := range []string{"background", "quick", "sync"} {
		t.Run(name, func(t *testing.T) {
			e := newHierarchyExecutor(t)
			dir := t.TempDir()
			in, out := filepath.Join(dir, "request.json"), filepath.Join(dir, "response.json")
			if err := e.store.AddMoneypenny(&store.Moneypenny{
				Name: "mac", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: in, FIFOOut: out,
			}); err != nil {
				t.Fatal(err)
			}
			old := mpSessionInfo{SessionID: "session", Name: "Original name", Agent: "claude", Status: "working"}
			e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{"session": old, "deleted": {SessionID: "deleted"}})
			var broadcasts atomic.Int32
			e.BroadcastFunc = func(*protocol.Response) { broadcasts.Add(1) }
			refresh := func() {
				switch name {
				case "quick":
					e.refreshMPSessionsQuick([]string{"mac"})
					waitForMPRefresh(t, e)
				case "sync":
					e.SyncSessions(log.New(io.Discard, "", 0))
				default:
					e.refreshMPSessions([]string{"mac"})
				}
			}
			refresh() // Missing transport files force an immediate error.
			got := e.getMPData()["mac"]["session"]
			if got.Name != old.Name || got.Agent != old.Agent || got.Status != "offline" {
				t.Fatalf("failed refresh discarded metadata or retained live status: %#v", got)
			}
			if broadcasts.Load() != 1 {
				t.Fatalf("failure did not broadcast a refresh: %d", broadcasts.Load())
			}
			refresh() // Cooldown must not cause a dashboard broadcast/reload loop.
			if broadcasts.Load() != 1 {
				t.Fatal("repeated failure caused redundant broadcasts")
			}
			e.clientManager.mu.Lock()
			e.clientManager.cooldowns["mac"] = time.Now().Add(-time.Second)
			e.clientManager.mu.Unlock()
			if e.clientManager.IsInCooldown("mac") || !e.clientManager.IsUnavailable("mac") {
				t.Fatal("expiry must permit retries without claiming the MP recovered")
			}
			if e.getMPData()["mac"]["session"].Status != "offline" {
				t.Fatal("expired cooldown resurrected stale live status")
			}

			if err := os.WriteFile(in, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, []byte("{\"status\":\"ok\",\"data\":{}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			refresh()
			if !e.clientManager.IsUnavailable("mac") || e.getMPData()["mac"]["session"].Name != old.Name {
				t.Fatal("malformed session data must not clear the failure or replace metadata")
			}
			e.clientManager.mu.Lock()
			e.clientManager.cooldowns["mac"] = time.Now().Add(-time.Second)
			e.clientManager.mu.Unlock()

			fresh := mpSessionInfo{SessionID: "session", Name: "Updated name", Agent: "copilot", Status: "idle"}
			data, err := json.Marshal([]mpSessionInfo{fresh})
			if err != nil {
				t.Fatal(err)
			}
			response, err := json.Marshal(struct {
				Status string          `json:"status"`
				Data   json.RawMessage `json:"data"`
			}{"ok", data})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, append(response, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			refresh()
			if e.clientManager.IsUnavailable("mac") {
				t.Fatal("successful refresh did not restore availability")
			}
			gotData := e.getMPData()["mac"]
			if len(gotData) != 1 || gotData["session"] != fresh {
				t.Fatalf("successful refresh must replace obsolete metadata: %#v", gotData)
			}
			if name == "sync" {
				e.cacheManager.SetRefreshing(true)
				rows := dashboardTable(t, e.Dashboard(nil))
				if len(rows["session"]) == 0 || rows["session"][1] != fresh.Name || rows["session"][8] != fresh.Agent {
					t.Fatalf("sync did not retain adopted session metadata for dashboard: %v", rows)
				}
			}
		})
	}
}

func TestDashboardOnlineAttentionUnchanged(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mac", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	cached := make(map[string]mpSessionInfo)
	for _, id := range []string{"ready", "idle", "working", "completed"} {
		if err := e.store.TrackSession(id, "mac"); err != nil {
			t.Fatal(err)
		}
		status := "idle"
		if id == "working" {
			status = "working"
		}
		cached[id] = mpSessionInfo{SessionID: id, Name: id, Status: status}
	}
	if err := e.store.SetSessionReviewed("idle", true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSessionHemStatus("completed", "completed"); err != nil {
		t.Fatal(err)
	}
	e.cacheManager.UpdateMP("mac", cached)
	e.cacheManager.SetRefreshing(true)
	rows := dashboardTable(t, e.Dashboard([]string{"--all"}))
	for id, want := range map[string]string{
		"ready": "ready (active)", "idle": "idle (active)",
		"working": "working (active)", "completed": "ready (completed)",
	} {
		if len(rows[id]) == 0 || rows[id][3] != want {
			t.Errorf("%s: got %v, want %s", id, rows[id], want)
		}
	}
}
