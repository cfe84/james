package ui

import (
	"testing"
)

func TestUnifySubagentCategories(t *testing.T) {
	tests := []struct {
		name     string
		entries  []dashboardEntry
		wantCats []int // expected Category for each entry
	}{
		{
			name: "parent idle with working subagent → all WORKING",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "RTMP-in", MPStatus: "idle", Category: 2},
				{SessionID: "sub1", Name: "↳ limit-instances", MPStatus: "working", Category: 1, ParentSessionID: "parent1"},
			},
			wantCats: []int{1, 1},
		},
		{
			name: "parent idle with ready subagent → all READY",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "my-agent", MPStatus: "idle", Category: 2},
				{SessionID: "sub1", Name: "↳ sub-ready", MPStatus: "ready", Category: 0, ParentSessionID: "parent1"},
			},
			wantCats: []int{0, 0},
		},
		{
			name: "parent idle with ready and working subagents → all READY (ready takes priority)",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "my-agent", MPStatus: "idle", Category: 2},
				{SessionID: "sub1", Name: "↳ sub-working", MPStatus: "working", Category: 1, ParentSessionID: "parent1"},
				{SessionID: "sub2", Name: "↳ sub-ready", MPStatus: "ready", Category: 0, ParentSessionID: "parent1"},
			},
			wantCats: []int{0, 0, 0},
		},
		{
			name: "parent idle with idle subagents → all stay IDLE",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "my-agent", MPStatus: "idle", Category: 2},
				{SessionID: "sub1", Name: "↳ sub-idle", MPStatus: "idle", Category: 2, ParentSessionID: "parent1"},
			},
			wantCats: []int{2, 2},
		},
		{
			name: "parent working with working subagent → all WORKING",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "my-agent", MPStatus: "working", Category: 1},
				{SessionID: "sub1", Name: "↳ sub-working", MPStatus: "working", Category: 1, ParentSessionID: "parent1"},
			},
			wantCats: []int{1, 1},
		},
		{
			name: "multiple parents each with subagents",
			entries: []dashboardEntry{
				{SessionID: "p1", Name: "agent-a", MPStatus: "idle", Category: 2},
				{SessionID: "s1", Name: "↳ sub-a", MPStatus: "working", Category: 1, ParentSessionID: "p1"},
				{SessionID: "p2", Name: "agent-b", MPStatus: "idle", Category: 2},
				{SessionID: "s2", Name: "↳ sub-b", MPStatus: "idle", Category: 2, ParentSessionID: "p2"},
			},
			wantCats: []int{1, 1, 2, 2},
		},
		{
			name: "parent with no subagents stays unchanged",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "solo-agent", MPStatus: "idle", Category: 2},
			},
			wantCats: []int{2},
		},
		{
			name: "completed parent with completed subagent → stays COMPLETED",
			entries: []dashboardEntry{
				{SessionID: "parent1", Name: "done-agent", MPStatus: "idle", HemStatus: "completed", Category: 3},
				{SessionID: "sub1", Name: "↳ done-sub", MPStatus: "idle", HemStatus: "completed", Category: 3, ParentSessionID: "parent1"},
			},
			wantCats: []int{3, 3},
		},
		{
			name: "entries re-sorted by unified category",
			entries: []dashboardEntry{
				// First group: parent idle, sub idle → stays IDLE (cat 2)
				{SessionID: "p1", Name: "idle-agent", MPStatus: "idle", Category: 2},
				{SessionID: "s1", Name: "↳ idle-sub", MPStatus: "idle", Category: 2, ParentSessionID: "p1"},
				// Second group: parent idle, sub working → becomes WORKING (cat 1)
				{SessionID: "p2", Name: "promoted-agent", MPStatus: "idle", Category: 2},
				{SessionID: "s2", Name: "↳ working-sub", MPStatus: "working", Category: 1, ParentSessionID: "p2"},
			},
			// After unification + re-sort: WORKING group (p2,s2) comes before IDLE (p1,s1)
			wantCats: []int{1, 1, 2, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := unifySubagentCategories(tt.entries)
			if len(result) != len(tt.wantCats) {
				t.Fatalf("got %d entries, want %d", len(result), len(tt.wantCats))
			}
			for i, want := range tt.wantCats {
				if result[i].Category != want {
					t.Errorf("entry[%d] (%s): Category=%d, want %d",
						i, result[i].Name, result[i].Category, want)
				}
			}
		})
	}
}
