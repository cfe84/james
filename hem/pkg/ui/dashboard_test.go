package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/protocol"
)

func TestWorkstreamDashboardExpansionFilterAndSelection(t *testing.T) {
	data := protocol.DashboardResult{Workstreams: []protocol.Workstream{
		{RootSessionID: "root", Section: "needs_you", Active: 3, Running: 1, Unread: 1, Sessions: []protocol.DashboardSession{
			{SessionID: "root", Name: "Research", Nick: "ian", Execution: "idle", Agent: "copilot", Model: "selected-model", Moneypenny: "mac"},
			{SessionID: "child", ParentSessionID: "root", Name: "Collect", Execution: "working", Depth: 1, Moneypenny: "windows"},
			{SessionID: "nested", ParentSessionID: "child", Name: "Findings", Execution: "idle", Unread: true, Depth: 2},
		}},
		{RootSessionID: "quiet", Section: "quiet", Active: 1, Sessions: []protocol.DashboardSession{
			{SessionID: "quiet", Name: "Later", Execution: "idle"},
		}},
	}}
	sender := &permissionTestSender{response: protocol.OKResponse(data)}
	m := newDashboardModel(&client{sender: sender})
	m.width, m.height = 120, 40
	loaded := m.loadDashboard()().(dashboardLoadedMsg)
	if !loaded.workstreams || !strings.Contains(strings.Join(sender.request.Args, " "), "--view=workstreams") {
		t.Fatal("client did not request/decode typed workstreams")
	}
	m, _ = m.Update(loaded)
	if len(m.filteredEntries()) != 2 {
		t.Fatal("workstreams should start collapsed")
	}
	view := m.View()
	for _, want := range []string{"NEEDS YOU", "ian · Research", "idle", "1 running", "1 unread", "selected-model"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q: %s", want, view)
		}
	}
	parentKeys := Model{client: m.client, dashboard: m, currentView: viewDashboard}
	for _, key := range []rune{'l', 'l', 'h', 'h'} {
		updated, cmd := parentKeys.updateDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		parentKeys = updated.(Model)
		if cmd != nil || parentKeys.currentView != viewDashboard || parentKeys.dashboard.expanded["root"] != (key == 'l') {
			t.Fatalf("%c did not fold/unfold the parent in place", key)
		}
	}
	allSessions, cmd := parentKeys.updateDashboard(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'L'}})
	if allSessions.(Model).currentView != viewSessions || cmd == nil {
		t.Fatal("L did not retain access to all sessions")
	}
	m = parentKeys.dashboard
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if len(m.filteredEntries()) != 4 {
		t.Fatal("expansion lost nested descendants")
	}
	m.cursor = 1
	parent := Model{client: m.client, dashboard: m, currentView: viewDashboard}
	opened, _ := parent.updateDashboard(tea.KeyMsg{Type: tea.KeyEnter})
	chats := opened.(Model)
	if chats.chat.moneypennyName != "windows" || len(chats.parentChats) != 0 || !chats.chat.isSubagent {
		t.Fatal("list entry should open the child directly without a parent navigation frame")
	}
	m.cursor = 2
	if m.selectedEntry().SessionID != "nested" {
		t.Fatal("cannot select descendant")
	}
	reordered := append([]dashboardEntry{loaded.entries[3]}, loaded.entries[:3]...)
	m, _ = m.Update(dashboardLoadedMsg{entries: reordered, workstreams: true})
	if m.selectedEntry().SessionID != "nested" || !m.expanded["root"] {
		t.Fatal("refresh lost selected identity or expansion")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedEntry().SessionID != "root" || len(m.filteredEntries()) != 2 {
		t.Fatal("collapse should select the root")
	}
	m.filterText = "findings"
	if got := m.filteredEntries(); len(got) != 3 || got[0].SessionID != "root" || got[1].SessionID != "child" {
		t.Fatalf("filter lost ancestor context: %+v", got)
	}
	m.filterText = ""
	m.toggleAllWorkstreams()
	if len(m.filteredEntries()) != 4 {
		t.Fatal("expand all did not reuse workstream hierarchy")
	}
}

func TestDashboardCompatibilityDoesNotMaskErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		view    bool
	}{
		{"flag provided but not defined: -view", false},
		{"database unavailable", true},
	} {
		sender := &permissionTestSender{response: protocol.ErrResponse(tc.message)}
		m := newDashboardModel(&client{sender: sender})
		m.projectFilter = "project"
		loaded := m.loadDashboard()().(dashboardLoadedMsg)
		// The fixed response fails both attempts; the last request shows whether
		// the client retried without the view flag or preserved the real error.
		if got := strings.Contains(strings.Join(sender.request.Args, " "), "--view"); got != tc.view {
			t.Fatalf("%q: last request %+v", tc.message, sender.request)
		}
		if loaded.err == nil || loaded.err.Error() != tc.message || loaded.projectFilter != "project" {
			t.Fatalf("error/context lost: %+v", loaded)
		}
	}
}

func TestListSubsessionBackReturnsToOrigin(t *testing.T) {
	c := &client{sender: &permissionTestSender{}}
	for _, origin := range []view{viewDashboard, viewProjectDetail, viewSessions} {
		t.Run(fmt.Sprint(origin), func(t *testing.T) {
			dashboard := newDashboardModel(c)
			dashboard.entries = []dashboardEntry{{
				SessionID: "child", ParentSessionID: "parent", Name: "Child", Moneypenny: "windows",
			}}
			m := Model{
				client: c, currentView: origin, dashboard: dashboard, projectDetail: dashboard,
				sessions:    sessionsModel{client: c, sessions: []sessionInfo{{SessionID: "child", Name: "Child", Moneypenny: "windows"}}},
				parentChats: []chatModel{newChatModel(c, "stale", "Stale", "mac")},
			}
			var opened tea.Model
			switch origin {
			case viewDashboard:
				opened, _ = m.updateDashboard(tea.KeyMsg{Type: tea.KeyEnter})
			case viewProjectDetail:
				opened, _ = m.updateProjectDetail(tea.KeyMsg{Type: tea.KeyEnter})
			case viewSessions:
				opened, _ = m.updateSessions(tea.KeyMsg{Type: tea.KeyEnter})
			}
			m = opened.(Model)
			if len(m.parentChats) != 0 || m.chat.sessionID != "child" || m.chat.moneypennyName != "windows" {
				t.Fatal("list entry invented a parent navigation frame")
			}
			// A genuine child visit still returns to the conversation first.
			nested, _ := m.Update(chatOpenSubagentMsg{sessionID: "nested", name: "Nested"})
			m = nested.(Model)
			m.chat.commandMode = true
			back, _ := m.updateChat(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			m = back.(Model)
			if m.currentView != viewChat || m.chat.sessionID != "child" {
				t.Fatal("conversation-origin Back lost its real parent")
			}
			m.chat.commandMode = true
			back, _ = m.updateChat(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			m = back.(Model)
			if m.currentView != origin || len(m.parentChats) != 0 {
				t.Fatal("list-origin Back did not return to its list")
			}
		})
	}
}

func TestChatDistinguishesAgentMessagesFromHumanMessages(t *testing.T) {
	m := chatModel{
		sessionName: "receiver", width: 100, height: 30,
		conversation: []conversationTurn{
			{Role: "user", Content: "from agent", SourceSessionID: "sender", SourceName: "cecil"},
			{Role: "user", Content: "from human"},
			{Role: "assistant", Content: "reply"},
		},
	}
	got := m.View()
	for _, label := range []string{"🕴️ cecil", "🧑‍💻 you", "🕴️ receiver"} {
		if !strings.Contains(got, label) {
			t.Errorf("chat missing %q: %q", label, got)
		}
	}
	if !strings.Contains(got, agentUserMsgStyle.Render("🕴️ cecil")) {
		t.Fatal("agent-sourced message header lost red styling")
	}
}

func TestDashboardDisplaysSelectedModel(t *testing.T) {
	for _, tc := range []struct {
		model, want string
	}{
		{"azure/gpt-6-luna", "opencode · azure/gpt-6-luna"},
		{"", "opencode"},
	} {
		m := dashboardModel{entries: []dashboardEntry{{
			SessionID: "session", Name: "session", Agent: "opencode", Model: tc.model,
			MPStatus: "idle", Category: 2,
		}}, width: 120}
		if got := m.View(); !strings.Contains(got, tc.want) {
			t.Fatalf("model %q missing from dashboard: %q", tc.model, got)
		}
	}
}

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

func TestCompactionThresholdFieldVisibilityAndNavigation(t *testing.T) {
	fields := []formField{
		{flag: "--compaction", value: "agent"},
		{flag: compactionThresholdFlag, value: "10"},
		{flag: "--model", value: ""},
	}
	if isFormFieldVisible(fields, 1) {
		t.Fatal("threshold should be hidden in agent mode")
	}
	if got := moveFormCursor(fields, 0, 1); got != 2 {
		t.Fatalf("moveFormCursor() = %d, want 2", got)
	}
	fields[0].value = "custom"
	if !isFormFieldVisible(fields, 1) {
		t.Fatal("threshold should be visible in custom mode")
	}
	if got := moveFormCursor(fields, 0, 1); got != 1 {
		t.Fatalf("moveFormCursor() = %d, want 1", got)
	}
	if fields[1].value != "10" {
		t.Fatalf("threshold value = %q, want preserved value", fields[1].value)
	}
}

func TestCompactionThresholdDefaultTracksContextUntilEdited(t *testing.T) {
	fields := []formField{
		{flag: "--context", value: ""},
		{flag: compactionThresholdFlag, value: "150", defaultDerived: true},
	}
	fields[0].value = "long_context"
	syncDefaultCompactionThreshold(fields)
	if fields[1].value != "800" {
		t.Fatalf("derived threshold = %q, want 800", fields[1].value)
	}
	fields[1].defaultDerived = false
	fields[1].value = "123"
	fields[0].value = ""
	syncDefaultCompactionThreshold(fields)
	if fields[1].value != "123" {
		t.Fatalf("edited threshold changed with context: %q", fields[1].value)
	}
}

func TestCompactionThresholdFormValueConvertsThousandsToTokens(t *testing.T) {
	for _, test := range []struct {
		display string
		tokens  string
	}{
		{display: "150", tokens: "150000"},
		{display: "123.456", tokens: "123456"},
	} {
		field := formField{flag: compactionThresholdFlag, value: test.display}
		if got := formFieldArgumentValue(field); got != test.tokens {
			t.Fatalf("formFieldArgumentValue(%q) = %q, want %q", test.display, got, test.tokens)
		}
	}
	if got := compactionThresholdDisplayValue(123456); got != "123.456" {
		t.Fatalf("compactionThresholdDisplayValue() = %q, want 123.456", got)
	}
}
