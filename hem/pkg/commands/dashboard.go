package commands

import (
	"log"
	"sort"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

func (e *Executor) dashboardMPData(sessions []*store.Session) map[string]map[string]mpSessionInfo {
	names := make(map[string]bool)
	for _, s := range sessions {
		names[s.MoneypennyName] = true
	}
	mpNames := make([]string, 0, len(names))
	data := e.getMPData()
	hasData := false
	for name := range names {
		mpNames = append(mpNames, name)
		if _, ok := data[name]; ok {
			hasData = true
		}
	}
	if !hasData && len(mpNames) > 0 {
		e.refreshMPSessionsQuick(mpNames)
		return e.getMPData()
	}
	e.ensureMPRefresh(mpNames)
	return data
}

func (e *Executor) dashboardSessionInfo(s *store.Session, data map[string]map[string]mpSessionInfo) mpSessionInfo {
	info, found := data[s.MoneypennyName][s.SessionID]
	if !found {
		info.Status = "unknown"
		log.Printf("dashboard: session %s not found on moneypenny %q", s.SessionID, s.MoneypennyName)
	}
	if _, ok := data[s.MoneypennyName]; !ok || e.clientManager.IsUnavailable(s.MoneypennyName) {
		info.Status = "offline"
	}
	if info.CreatedAt == "" && !s.CreatedAt.IsZero() {
		info.CreatedAt = s.CreatedAt.UTC().Format(time.RFC3339)
	}
	if info.Name == "" {
		info.Name = s.SessionID
		if len(info.Name) > 12 {
			info.Name = info.Name[:12] + "..."
		}
	}
	return info
}

func (e *Executor) workstreamDashboard(tracked []*store.Session, projectNames map[string]string, project string, showAll bool) *protocol.Response {
	disabled := e.disabledMPs()
	eligible := make([]*store.Session, 0, len(tracked))
	for _, s := range tracked {
		if !disabled[s.MoneypennyName] {
			eligible = append(eligible, s)
		}
	}
	data := e.dashboardMPData(eligible)
	sessions := make([]protocol.DashboardSession, 0, len(eligible))
	for _, s := range eligible {
		info := e.dashboardSessionInfo(s, data)
		sessions = append(sessions, protocol.DashboardSession{
			SessionID: s.SessionID, ParentSessionID: s.ParentSessionID,
			Name: info.Name, Nick: s.Nick, Project: projectNames[s.ProjectID],
			Moneypenny: s.MoneypennyName, Agent: info.Agent, Model: info.Model,
			Execution: info.Status, Unread: !s.Reviewed, Completed: s.HemStatus == "completed",
			CreatedAt: info.CreatedAt, LastActivity: info.LastAccessed,
		})
		state := info.Status
		if state == "idle" && !s.Reviewed {
			state = "ready"
		}
		e.watchManager.SetLastState(s.SessionID, state)
	}
	return protocol.OKResponse(buildWorkstreams(sessions, project, showAll))
}

func dashboardActivity(s protocol.DashboardSession) time.Time {
	stamp := s.LastActivity
	if stamp == "" {
		stamp = s.CreatedAt
	}
	t, _ := time.Parse(time.RFC3339Nano, stamp)
	return t
}

func buildWorkstreams(sessions []protocol.DashboardSession, project string, showAll bool) protocol.DashboardResult {
	byID := make(map[string]protocol.DashboardSession, len(sessions))
	parents := make(map[string]string, len(sessions))
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		byID[s.SessionID] = s
		ids = append(ids, s.SessionID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := byID[id]
		parent := s.ParentSessionID
		if _, found := byID[parent]; parent != "" && !found {
			s.HierarchyIssue = "missing_parent"
			parent = ""
		}
		parents[id] = parent
		byID[id] = s
	}
	// Break one edge per cycle deterministically, keeping every session visible.
	done := make(map[string]bool)
	for _, id := range ids {
		path := []string{}
		seen := make(map[string]int)
		for current := id; current != "" && !done[current]; current = parents[current] {
			if start, cycle := seen[current]; cycle {
				root := current
				for _, member := range path[start:] {
					if member < root {
						root = member
					}
					s := byID[member]
					s.HierarchyIssue = "cycle"
					byID[member] = s
				}
				parents[root] = ""
				break
			}
			seen[current] = len(path)
			path = append(path, current)
		}
		for _, member := range path {
			done[member] = true
		}
	}
	children := make(map[string][]string)
	for _, id := range ids {
		children[parents[id]] = append(children[parents[id]], id)
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			a, b := children[parent][i], children[parent][j]
			at, bt := dashboardActivity(byID[a]), dashboardActivity(byID[b])
			if !at.Equal(bt) {
				return at.After(bt)
			}
			return a < b
		})
	}
	result := protocol.DashboardResult{Workstreams: []protocol.Workstream{}}
	for _, root := range children[""] {
		w := protocol.Workstream{RootSessionID: root}
		// Keep ancestors of matching/live descendants, including completed bridges.
		var collect func(string, int, bool) []protocol.DashboardSession
		collect = func(id string, depth int, parentMatches bool) []protocol.DashboardSession {
			s := byID[id]
			matches := parentMatches || project == "" || s.Project == project
			descendants := []protocol.DashboardSession{}
			for _, child := range children[id] {
				descendants = append(descendants, collect(child, depth+1, matches)...)
			}
			if len(descendants) == 0 && (!matches || (s.Completed && !showAll)) {
				return nil
			}
			s.Depth = depth
			return append([]protocol.DashboardSession{s}, descendants...)
		}
		w.Sessions = collect(root, 0, false)
		if len(w.Sessions) == 0 {
			continue
		}
		var latest time.Time
		for _, s := range w.Sessions {
			if t := dashboardActivity(s); t.After(latest) {
				latest = t
				w.LastActivity = t.UTC().Format(time.RFC3339Nano)
			}
			if s.Completed {
				continue
			}
			w.Active++
			if s.Unread {
				w.Unread++
			}
			if s.Execution == "working" {
				w.Running++
			}
			if s.Execution == "offline" || s.Execution == "unknown" {
				w.Unavailable++
			}
		}
		switch {
		case w.Active == 0:
			w.Section = "completed"
		case w.Unread > 0:
			w.Section = "needs_you"
		case w.Running > 0:
			w.Section = "in_progress"
		default:
			w.Section = "quiet"
		}
		result.Workstreams = append(result.Workstreams, w)
	}
	rank := map[string]int{"needs_you": 0, "in_progress": 1, "quiet": 2, "completed": 3}
	sort.Slice(result.Workstreams, func(i, j int) bool {
		a, b := result.Workstreams[i], result.Workstreams[j]
		if rank[a.Section] != rank[b.Section] {
			return rank[a.Section] < rank[b.Section]
		}
		if a.LastActivity != b.LastActivity {
			at, _ := time.Parse(time.RFC3339Nano, a.LastActivity)
			bt, _ := time.Parse(time.RFC3339Nano, b.LastActivity)
			return at.After(bt)
		}
		return a.RootSessionID < b.RootSessionID
	})
	return result
}
