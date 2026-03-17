package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dashboardPollTickMsg struct{}

const dashboardPollInterval = 5 * time.Second

func dashboardPollTick() tea.Cmd {
	return tea.Tick(dashboardPollInterval, func(time.Time) tea.Msg {
		return dashboardPollTickMsg{}
	})
}

// dashboardEntry represents a session in the dashboard.
type dashboardEntry struct {
	SessionID       string
	Name            string
	Project         string
	MPStatus        string // ready/idle/working/offline
	HemStatus       string // active/completed
	SubInfo         string // e.g. "[3 subs, 1 ready]"
	Moneypenny      string
	CreatedAt       string
	LastActive      string
	Category        int // 0=READY, 1=WORKING, 2=IDLE, 3=COMPLETED
	ParentSessionID string // non-empty for subagent entries
}

// dashboardModel displays the attention-based dashboard.
// When projectFilter is set, it shows only sessions for that project.
type dashboardModel struct {
	entries       []dashboardEntry
	cursor        int
	width         int
	height        int
	err           error
	loading       bool
	showAll       bool
	showSubs      bool
	client        *client
	projectFilter string // project name to filter by (empty = all)
	title         string // custom title (e.g. project name)
	filtering     bool   // true when filter input is active
	filterText    string // current filter text (case insensitive match on name)
}

type dashboardLoadedMsg struct {
	entries       []dashboardEntry
	err           error
	projectFilter string // which dashboard instance this belongs to
}

type sessionCompletedMsg struct{ err error }
type dashboardDeletedMsg struct{ err error }

func newDashboardModel(c *client) dashboardModel {
	return dashboardModel{
		client:  c,
		loading: true,
	}
}

func (m dashboardModel) loadDashboard() tea.Cmd {
	projectFilter := m.projectFilter
	showAll := m.showAll
	showSubs := m.showSubs
	return func() tea.Msg {
		// Use the dashboard command which handles grouping and project filtering.
		args := []string{}
		if projectFilter != "" {
			args = append(args, "--project", projectFilter)
		}
		if showAll {
			args = append(args, "--all")
		}
		if showSubs {
			args = append(args, "--show-subs")
		}
		resp, err := m.client.send("dashboard", "", args...)
		if err != nil {
			return dashboardLoadedMsg{err: err, projectFilter: projectFilter}
		}
		if resp.Status == "error" {
			return dashboardLoadedMsg{err: fmt.Errorf("%s", resp.Message), projectFilter: projectFilter}
		}

		// Parse the TableResult.
		var table struct {
			Headers []string   `json:"headers"`
			Rows    [][]string `json:"rows"`
		}
		if err := json.Unmarshal(resp.Data, &table); err != nil {
			return dashboardLoadedMsg{err: fmt.Errorf("parsing dashboard: %w", err)}
		}

		var entries []dashboardEntry
		for _, row := range table.Rows {
			e := dashboardEntry{}
			if len(row) > 0 {
				e.SessionID = row[0]
			}
			if len(row) > 1 {
				e.Name = row[1]
			}
			if len(row) > 2 {
				e.Project = row[2]
			}
			if len(row) > 3 {
				// Status format: "working (active) [3 subs, 1 ready]"
				e.MPStatus = row[3]
				e.HemStatus = "active"
				if strings.Contains(row[3], "(completed)") {
					e.HemStatus = "completed"
				}
				if bracketIdx := strings.Index(row[3], " ["); bracketIdx >= 0 {
					e.SubInfo = row[3][bracketIdx+1:]
				}
				if idx := strings.Index(row[3], " ("); idx >= 0 {
					e.MPStatus = row[3][:idx]
				}
			}
			if len(row) > 4 {
				e.Moneypenny = row[4]
			}
			if len(row) > 5 {
				e.CreatedAt = row[5]
			}
			if len(row) > 6 {
				e.LastActive = row[6]
			}
			if len(row) > 7 {
				e.ParentSessionID = row[7]
			}

			// Determine category from parsed status.
			// 0=READY, 1=WORKING, 2=IDLE, 3=COMPLETED
			e.Category = 1 // WORKING
			if e.HemStatus == "completed" {
				e.Category = 3
			} else if e.MPStatus == "ready" {
				e.Category = 0
			} else if e.MPStatus == "idle" || e.MPStatus == "offline" || e.MPStatus == "unknown" {
				e.Category = 2
			}

			entries = append(entries, e)
		}

		// Unify the category for each parent+children group so they appear
		// in the same dashboard section. This runs always because the server
		// sends working/ready subagents even without --show-subs.
		// - If any subagent is Ready → whole group is Ready
		// - Else if any subagent is Working → whole group is Working
		// - Else use the parent's own category
		entries = unifySubagentCategories(entries)

		return dashboardLoadedMsg{entries: entries, projectFilter: projectFilter}
	}
}

func (m dashboardModel) deleteSession(id string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.send("delete", "session", id)
		if err != nil {
			return dashboardDeletedMsg{err: err}
		}
		if resp.Status == "error" {
			return dashboardDeletedMsg{err: fmt.Errorf("%s", resp.Message)}
		}
		return dashboardDeletedMsg{err: nil}
	}
}

func (m dashboardModel) completeSession(id string) tea.Cmd {
	return func() tea.Msg {
		// Send complete session command via hem protocol.
		resp, err := m.client.send("complete", "session", id)
		if err != nil {
			return sessionCompletedMsg{err: err}
		}
		if resp.Status == "error" {
			return sessionCompletedMsg{err: fmt.Errorf("%s", resp.Message)}
		}
		return sessionCompletedMsg{err: nil}
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(m.loadDashboard(), dashboardPollTick())
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case dashboardPollTickMsg:
		if !m.loading {
			return m, tea.Batch(m.loadDashboard(), dashboardPollTick())
		}
		return m, dashboardPollTick()

	case dashboardLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			// Don't wipe entries on error — keep last good data.
		} else {
			m.entries = msg.entries
			m.err = nil
			if m.cursor >= len(m.entries) {
				m.cursor = max(0, len(m.entries)-1)
			}
		}

	case sessionCompletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		m.loading = true
		return m, m.loadDashboard()

	case dashboardDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		m.loading = true
		return m, m.loadDashboard()

	case tea.KeyMsg:
		// Filter input mode.
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filterText = ""
				m.cursor = 0
			case "enter":
				m.filtering = false
				m.cursor = 0
			case "backspace":
				if len(m.filterText) > 0 {
					m.filterText = m.filterText[:len(m.filterText)-1]
					m.cursor = 0
				}
			case "ctrl+u":
				m.filterText = ""
				m.cursor = 0
			default:
				if msg.Type == tea.KeyRunes {
					m.filterText += string(msg.Runes)
					m.cursor = 0
				} else if msg.String() == " " {
					m.filterText += " "
					m.cursor = 0
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			filtered := m.filteredEntries()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case "/":
			m.filtering = true
			m.filterText = ""
			m.cursor = 0
		case "a":
			m.showAll = !m.showAll
			m.loading = true
			return m, m.loadDashboard()
		case "s":
			m.showSubs = !m.showSubs
			m.loading = true
			return m, m.loadDashboard()
		case "r":
			m.loading = true
			return m, m.loadDashboard()
		}
	}
	return m, nil
}

// filteredEntries returns entries matching the current filter text.
func (m dashboardModel) filteredEntries() []dashboardEntry {
	if m.filterText == "" {
		return m.entries
	}
	ft := strings.ToLower(m.filterText)
	var result []dashboardEntry
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.Name), ft) {
			result = append(result, e)
		}
	}
	return result
}

func (m dashboardModel) selectedEntry() *dashboardEntry {
	filtered := m.filteredEntries()
	if len(filtered) == 0 || m.cursor >= len(filtered) {
		return nil
	}
	return &filtered[m.cursor]
}

var (
	categoryReadyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Bold(true)
	categoryWorkingStyle = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	categoryIdleStyle    = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	categoryDoneStyle    = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)

	statusCompleted = lipgloss.NewStyle().Foreground(colorMuted).Render("✓ done")
)

// unifySubagentCategories ensures a parent and all its subagents share the same
// dashboard category. Priority: Ready > Working > parent's own category.
func unifySubagentCategories(entries []dashboardEntry) []dashboardEntry {
	// Identify groups: a parent followed by its subagents.
	i := 0
	for i < len(entries) {
		e := entries[i]
		if e.ParentSessionID != "" {
			// Orphan subagent — skip.
			i++
			continue
		}
		// Find all subagents that follow this parent.
		groupStart := i
		i++
		for i < len(entries) && entries[i].ParentSessionID == e.SessionID {
			i++
		}
		groupEnd := i
		if groupEnd-groupStart <= 1 {
			// No subagents in this group.
			continue
		}
		// Determine the unified category from subagent statuses.
		hasReady := false
		hasWorking := false
		for j := groupStart + 1; j < groupEnd; j++ {
			switch entries[j].Category {
			case 0: // READY
				hasReady = true
			case 1: // WORKING
				hasWorking = true
			}
		}
		var groupCat int
		if hasReady {
			groupCat = 0 // READY
		} else if hasWorking {
			groupCat = 1 // WORKING
		} else {
			groupCat = entries[groupStart].Category // parent's own
		}
		// Apply to entire group.
		for j := groupStart; j < groupEnd; j++ {
			entries[j].Category = groupCat
		}
	}

	// Re-sort by category while keeping group order stable.
	// Bucket by category, preserving order within each bucket.
	buckets := make([][]dashboardEntry, 4)
	i = 0
	for i < len(entries) {
		e := entries[i]
		cat := e.Category
		if e.ParentSessionID != "" {
			// Orphan — just add it.
			buckets[cat] = append(buckets[cat], e)
			i++
			continue
		}
		// Collect parent + its subagents.
		group := []dashboardEntry{e}
		i++
		for i < len(entries) && entries[i].ParentSessionID == e.SessionID {
			group = append(group, entries[i])
			i++
		}
		buckets[cat] = append(buckets[cat], group...)
	}

	result := make([]dashboardEntry, 0, len(entries))
	for _, bucket := range buckets {
		result = append(result, bucket...)
	}
	return result
}

func categoryLabel(cat int) string {
	switch cat {
	case 0:
		return categoryReadyStyle.Render(" READY ")
	case 1:
		return categoryWorkingStyle.Render(" WORKING ")
	case 2:
		return categoryIdleStyle.Render(" IDLE ")
	case 3:
		return categoryDoneStyle.Render(" COMPLETED ")
	}
	return ""
}

func (m dashboardModel) View() string {
	if m.loading {
		return "\n  Loading dashboard..."
	}
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v", m.err)
	}
	if len(m.entries) == 0 {
		if m.projectFilter != "" {
			return "\n  No sessions in this project. Press [n] to create one."
		}
		return "\n  No sessions. Press [n] to create one, or [l] to view all sessions."
	}

	entries := m.filteredEntries()

	var b strings.Builder

	if m.title != "" {
		b.WriteString(titleStyle.Render(" "+m.title+" "))
		b.WriteString("\n")
	}

	// Filter bar.
	if m.filtering {
		b.WriteString("  " + labelStyle.Render("Filter:") + " " + fieldActiveStyle.Render(m.filterText+"█"))
		b.WriteString("\n")
	} else if m.filterText != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("filter: %q (%d matches, / to edit, esc to clear)", m.filterText, len(entries))))
		b.WriteString("\n")
	}

	if len(entries) == 0 {
		b.WriteString("\n  No matching sessions.")
		return b.String()
	}

	// Available height for rows.
	maxRows := m.height - 4
	if m.title != "" {
		maxRows -= 1
	}
	if m.filtering || m.filterText != "" {
		maxRows -= 1
	}
	if maxRows < 1 {
		maxRows = 10
	}

	// Compute visible window.
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(entries) {
		end = len(entries)
	}

	// Show project column if not already filtered by project and any entry has a project.
	showProject := m.projectFilter == ""
	if showProject {
		hasAnyProject := false
		for _, e := range entries {
			if e.Project != "" {
				hasAnyProject = true
				break
			}
		}
		showProject = hasAnyProject
	}

	// Calculate column widths based on terminal width.
	// Fixed columns: indent(2) + status(10) + lastActive(14) + spacing(~8)
	// Flexible: name, moneypenny, project
	w := m.width
	if w < 80 {
		w = 80
	}
	fixedWidth := 2 + 10 + 14 + 8 // indent + status + lastActive + gaps
	if showProject {
		fixedWidth += 14 // project column + gap
	}
	flexible := w - fixedWidth
	nameWidth := flexible * 45 / 100
	mpWidth := flexible * 30 / 100
	projWidth := flexible * 25 / 100
	if nameWidth < 15 {
		nameWidth = 15
	}
	if mpWidth < 10 {
		mpWidth = 10
	}
	if projWidth < 10 {
		projWidth = 10
	}

	lastCat := -1

	for i := start; i < end; i++ {
		e := entries[i]

		// Category header.
		if e.Category != lastCat {
			if lastCat != -1 {
				b.WriteString("\n")
			}
			b.WriteString(categoryLabel(e.Category))
			b.WriteString("\n")
			lastCat = e.Category
		}

		// Entry line.
		name := truncate(e.Name, nameWidth)
		if name == "" {
			name = truncate(e.SessionID, nameWidth)
		}
		mp := truncate(e.Moneypenny, mpWidth)
		status := statusBadge(e.MPStatus)
		if e.SubInfo != "" {
			status += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(e.SubInfo)
		}
		lastActive := relativeTime(e.LastActive)
		if lastActive == "" {
			lastActive = relativeTime(e.CreatedAt)
		}

		nameFmt := fmt.Sprintf("%%-%ds", nameWidth+2)
		mpFmt := fmt.Sprintf("%%-%ds", mpWidth+2)

		var line string
		if showProject {
			project := truncate(e.Project, projWidth)
			if project == "" {
				project = "-"
			}
			projFmt := fmt.Sprintf("%%-%ds", projWidth+2)
			line = fmt.Sprintf("  "+nameFmt+projFmt+"%-10s "+mpFmt+"%s", name, project, status, mp, lastActive)
		} else {
			line = fmt.Sprintf("  "+nameFmt+"%-10s "+mpFmt+"%s", name, status, mp, lastActive)
		}

		if i == m.cursor {
			if m.width > 0 && lipgloss.Width(line) < m.width {
				line += strings.Repeat(" ", m.width-lipgloss.Width(line))
			}
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	if len(entries) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(entries))))
		b.WriteString("\n")
	}

	return b.String()
}

// relativeTime converts an absolute timestamp like "Jan 02 15:04" or
// "2006-01-02T15:04:05Z" into a human-friendly relative string like "2m ago".
func relativeTime(ts string) string {
	if ts == "" {
		return ""
	}
	var t time.Time
	var err error
	// Try formats the server might send.
	for _, layout := range []string{
		"Jan 02 15:04",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		t, err = time.Parse(layout, ts)
		if err == nil {
			break
		}
	}
	if err != nil {
		return ts // fallback to raw string
	}

	// "Jan 02 15:04" has no year — assume current year (or last year if in the future).
	if t.Year() == 0 {
		now := time.Now()
		t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
		if t.After(now.Add(24 * time.Hour)) {
			t = t.AddDate(-1, 0, 0)
		}
	}

	d := time.Since(t.Local())
	if d < 0 {
		return "just now"
	}

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		return t.Local().Format("Jan 02")
	}
}
