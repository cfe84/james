package commands

import (
	"context"
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
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/envelope"
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

func TestSynchronousResultDoesNotReopenAsUnread(t *testing.T) {
	e := newHierarchyExecutor(t)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "request.json"), filepath.Join(dir, "response.json")
	mp := &store.Moneypenny{Name: "mac", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: in, FIFOOut: out}
	if err := e.store.AddMoneypenny(mp); err != nil {
		t.Fatal(err)
	}
	if err := e.store.TrackSession("session", "mac"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Both metadata and conversation requests can consume this authoritative snapshot.
	response := `{"status":"ok","data":{"status":"idle","conversation":[{"role":"assistant","content":"Review complete."}],"revision":9,"generation":1}}` + "\n"
	if err := os.WriteFile(out, []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := e.pollUntilIdle(ctx, mp, "session")
	if err != nil || text != "Review complete." {
		t.Fatalf("synchronous result = %q, %v", text, err)
	}
	e.processSessionReadiness("mac", map[string]mpSessionInfo{
		"session": {SessionID: "session", Status: "idle", Revision: 9, Generation: 1, ResultRevision: 7},
	})
	session, err := e.store.GetSession("session")
	if err != nil || !session.Reviewed {
		t.Fatalf("synchronous response reopened as unread: %+v, %v", session, err)
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
			SessionID: id, Name: "Name " + id, Agent: "copilot", Model: "gpt-6-sol", Status: "idle",
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
		if len(row) != 11 || strings.TrimPrefix(row[1], "↳ ") != info.Name || row[8] != info.Agent || row[10] != info.Model ||
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
	resp := e.Dashboard([]string{"--view=workstreams"})
	var dashboard protocol.DashboardResult
	if resp.Status != "ok" || json.Unmarshal(resp.Data, &dashboard) != nil {
		t.Fatalf("workstream dashboard failed: %+v", resp)
	}
	seen := 0
	for _, w := range dashboard.Workstreams {
		if w.Running != 0 || w.Unavailable != len(w.Sessions) {
			t.Fatalf("offline counts claim live execution: %+v", w)
		}
		for _, s := range w.Sessions {
			seen++
			if s.Execution != "offline" || s.Name != cached[s.SessionID].Name || s.Model != "gpt-6-sol" {
				t.Fatalf("workstream lost offline metadata: %+v", s)
			}
		}
	}
	if seen != len(cached) {
		t.Fatalf("workstream has %d sessions, want %d", seen, len(cached))
	}
}

func TestWorkstreamProjection(t *testing.T) {
	base := []protocol.DashboardSession{
		{SessionID: "parent", Execution: "idle", Completed: true},
		{SessionID: "child", ParentSessionID: "parent", Execution: "working", Project: "selected"},
		{SessionID: "grandchild", ParentSessionID: "child", Execution: "idle", Unread: true, Project: "selected"},
		{SessionID: "done", ParentSessionID: "child", Execution: "working", Unread: true, Completed: true},
		{SessionID: "other", Execution: "idle", LastActivity: "2026-09-01T10:00:00Z"},
	}
	for _, tc := range []struct {
		name, project string
		all           bool
		streams, size int
	}{
		{"active", "", false, 2, 3},
		{"completed visible", "", true, 2, 4},
		{"project ancestor context", "selected", false, 1, 3},
		{"no matching project", "absent", true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildWorkstreams(base, tc.project, tc.all)
			if len(got.Workstreams) != tc.streams {
				t.Fatalf("streams = %+v", got.Workstreams)
			}
			if tc.streams == 0 {
				return
			}
			w := got.Workstreams[0]
			if w.RootSessionID != "parent" || len(w.Sessions) != tc.size ||
				w.Section != "needs_you" || w.Running != 1 || w.Unread != 1 || w.Active != 2 {
				t.Fatalf("wrong hierarchy/counts: %+v", w)
			}
			if w.Sessions[0].Execution != "idle" || !w.Sessions[0].Completed {
				t.Fatal("group state overwrote parent execution/completion")
			}
			for _, s := range w.Sessions {
				if s.SessionID == "grandchild" && s.Depth != 2 {
					t.Fatalf("lost nested depth: %+v", s)
				}
			}
		})
	}
}

func TestAttentionProjectionAndEventCache(t *testing.T) {
	report := envelope.SessionReport{Summary: "Checking rollout", Attention: &envelope.SessionAttention{
		Kind: "needs_answer", Reason: "Which region?", Revision: 1,
	}}
	e := newHierarchyExecutor(t)
	e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{"done": {
		SessionID: "done", Status: "idle", Revision: 3,
	}})
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()
	data, _ := json.Marshal(struct {
		envelope.SessionReport
		Revision int64 `json:"revision"`
	}{report, 4})
	e.handleMoneypennyEvent("mac", &transport.Response{Type: "notification", Event: envelope.EventSessionAttention, SessionID: "done", Data: data})
	select {
	case event := <-events:
		if event.Noun != "dashboard" {
			t.Fatalf("attention did not invalidate dashboard: %+v", event)
		}
	default:
		t.Fatal("attention did not publish a refresh")
	}
	// A list request started before the event must not roll its report back.
	e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{"done": {SessionID: "done", Status: "idle", Revision: 3}})
	got := e.cacheManager.GetSnapshot()["mac"]["done"]
	if got.Attention == nil || got.Attention.Reason != report.Attention.Reason || got.Revision != 4 {
		t.Fatalf("event report was lost: %+v", got)
	}
	result := buildWorkstreams([]protocol.DashboardSession{{
		SessionReport: got.SessionReport, SessionID: "done", Execution: "idle", Completed: true,
	}}, "", false)
	if len(result.Workstreams) != 1 || result.Workstreams[0].Section != "needs_you" ||
		result.Workstreams[0].Requests != 1 || result.Workstreams[0].Active != 0 || result.Workstreams[0].Unread != 0 {
		t.Fatalf("completion hid an unresolved request: %+v", result)
	}
	for _, args := range [][]string{nil, {"done"}, {"done", "--revision", "-1"}, {"done", "--revision", "bad"}} {
		if e.ResolveAttention(args).Status != protocol.StatusError {
			t.Fatalf("accepted unsafe resolution: %v", args)
		}
	}
}

func TestWorkstreamHierarchyAndOrdering(t *testing.T) {
	sessions := []protocol.DashboardSession{
		{SessionID: "cycle-b", ParentSessionID: "cycle-a", Execution: "idle"},
		{SessionID: "cycle-a", ParentSessionID: "cycle-b", Execution: "idle"},
		{SessionID: "orphan", ParentSessionID: "missing", Execution: "offline"},
		{SessionID: "self", ParentSessionID: "self", Execution: "unknown"},
		{SessionID: "running", Execution: "working", LastActivity: "2026-09-01T12:00:00Z"},
		{SessionID: "newer", Execution: "idle", LastActivity: "2026-09-01T12:00:00.1Z"},
		{SessionID: "older", Execution: "idle", LastActivity: "2026-09-01T14:00:00+02:00"},
		{SessionID: "done", Execution: "working", Unread: true, Completed: true},
	}
	got := buildWorkstreams(sessions, "", true)
	seen := make(map[string]bool)
	for _, w := range got.Workstreams {
		for _, s := range w.Sessions {
			if seen[s.SessionID] {
				t.Fatalf("duplicate session %s", s.SessionID)
			}
			seen[s.SessionID] = true
			switch s.SessionID {
			case "cycle-a", "cycle-b", "self":
				if s.HierarchyIssue != "cycle" {
					t.Fatalf("cycle not exposed: %+v", s)
				}
			case "orphan":
				if s.HierarchyIssue != "missing_parent" || s.Depth != 0 || w.Unavailable != 1 {
					t.Fatalf("orphan hidden or claims availability: %+v", w)
				}
			}
		}
	}
	if len(seen) != len(sessions) {
		t.Fatalf("lost sessions: %v", seen)
	}
	if got.Workstreams[0].RootSessionID != "running" ||
		got.Workstreams[1].RootSessionID != "newer" || got.Workstreams[2].RootSessionID != "older" {
		t.Fatalf("incorrect section/chronological ordering: %+v", got.Workstreams)
	}
	last := got.Workstreams[len(got.Workstreams)-1]
	if last.Section != "completed" || last.Running != 0 || last.Unread != 0 || last.Active != 0 {
		t.Fatalf("completed sessions counted as active: %+v", last)
	}
	// Input ordering must not change cycle roots or equal-activity ordering.
	for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	}
	again := buildWorkstreams(sessions, "", true)
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("projection depends on input order")
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
			if err := e.store.TrackSession("session", "mac"); err != nil {
				t.Fatal(err)
			}
			if err := e.store.ObserveVisible("session", 2, 1); err != nil {
				t.Fatal(err)
			}
			if _, err := e.store.AcknowledgeVisible("session", 2, 1); err != nil {
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

			fresh := mpSessionInfo{SessionID: "session", Name: "Updated name", Agent: "copilot", Status: "idle",
				Revision: 5, Generation: 1, ResultRevision: 4}
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
			e.cacheManager.SetRefreshing(true)
			var dashboard protocol.DashboardResult
			resp := e.Dashboard([]string{"--view=workstreams"})
			if resp.Status != protocol.StatusOK || json.Unmarshal(resp.Data, &dashboard) != nil ||
				len(dashboard.Workstreams) != 1 || dashboard.Workstreams[0].Section != "needs_you" ||
				dashboard.Workstreams[0].Unread != 1 || dashboard.Workstreams[0].Requests != 0 ||
				dashboard.Workstreams[0].Sessions[0].Execution != "idle" {
				t.Fatalf("unseen main-agent result did not surface as non-urgent unread: %s", resp.Data)
			}
			rows := dashboardTable(t, e.Dashboard(nil))
			if !strings.HasPrefix(rows["session"][3], "ready") {
				t.Fatalf("idle result is not Ready in the table: %v", rows)
			}
			if resp := e.AcknowledgeSession([]string{"session", "--revision=2", "--generation=1"}); resp.Status != protocol.StatusOK {
				t.Fatalf("stale ack: %+v", resp)
			}
			session, err := e.store.GetSession("session")
			if err != nil || session.Reviewed {
				t.Fatalf("delayed working ack hid result: %+v, %v", session, err)
			}
			if err := e.store.ObserveVisible("session", 5, 1); err != nil {
				t.Fatal(err)
			}
			if resp := e.AcknowledgeSession([]string{"session", "--revision=5", "--generation=1"}); resp.Status != protocol.StatusOK {
				t.Fatalf("rendered result ack: %+v", resp)
			}
			refresh()
			e.cacheManager.SetRefreshing(true)
			resp = e.Dashboard([]string{"--view=workstreams"})
			if resp.Status != protocol.StatusOK || json.Unmarshal(resp.Data, &dashboard) != nil ||
				len(dashboard.Workstreams) != 1 || dashboard.Workstreams[0].Section != "quiet" {
				t.Fatalf("reviewed result reopened after refresh: %s", resp.Data)
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

func TestDashboardWorkingParentKeepsWorkingStatusWithReadyChild(t *testing.T) {
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
	e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{
		"parent": {SessionID: "parent", Name: "parent", Status: "working"},
		"child":  {SessionID: "child", Name: "child", Status: "idle"},
	})
	e.cacheManager.SetRefreshing(true)

	rows := dashboardTable(t, e.Dashboard([]string{"--show-subs"}))
	if got := rows["parent"][3]; got != "working (active) [1 subs, 1 ready]" {
		t.Fatalf("parent status = %q, want working with ready-child summary", got)
	}
	if got := rows["child"][3]; got != "ready (active)" {
		t.Fatalf("child status = %q, want ready", got)
	}
}

func TestDashboardShowsAllActiveSubagentsByDefault(t *testing.T) {
	e := newHierarchyExecutor(t)
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mac", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.TrackSession("parent", "mac"); err != nil {
		t.Fatal(err)
	}
	for _, child := range []string{"ready-child", "idle-child", "completed-child"} {
		if err := e.store.TrackSubSession(child, "mac", "parent"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.SetSessionReviewed("idle-child", true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetSessionHemStatus("completed-child", "completed"); err != nil {
		t.Fatal(err)
	}
	e.cacheManager.UpdateMP("mac", map[string]mpSessionInfo{
		"parent":          {SessionID: "parent", Name: "parent", Status: "working"},
		"ready-child":     {SessionID: "ready-child", Name: "ready child", Status: "idle"},
		"idle-child":      {SessionID: "idle-child", Name: "idle child", Status: "idle"},
		"completed-child": {SessionID: "completed-child", Name: "completed child", Status: "idle"},
	})
	e.cacheManager.SetRefreshing(true)

	rows := dashboardTable(t, e.Dashboard(nil))
	if len(rows["ready-child"]) == 0 || len(rows["idle-child"]) == 0 {
		t.Fatalf("active subagents missing from default dashboard: %#v", rows)
	}
	if _, found := rows["completed-child"]; found {
		t.Fatalf("completed subagent shown without --show-subs: %#v", rows["completed-child"])
	}
}
