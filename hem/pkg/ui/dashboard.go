package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// dashboardEntry represents a session in the dashboard.
type dashboardEntry struct {
	SessionID  string
	Name       string
	Project    string
	MPStatus   string // ready/idle/working/offline
	HemStatus  string // active/completed
	Moneypenny string
	LastActive string
	Category   int // 0=READY, 1=WORKING, 2=IDLE, 3=COMPLETED
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
	client        *client
	projectFilter string // project name to filter by (empty = all)
	title         string // custom title (e.g. project name)
}

type dashboardLoadedMsg struct {
	entries []dashboardEntry
	err     error
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
	return func() tea.Msg {
		// Use the dashboard command which handles grouping and project filtering.
		args := []string{}
		if projectFilter != "" {
			args = append(args, "--project", projectFilter)
		}
		if showAll {
			args = append(args, "--all")
		}
		resp, err := m.client.send("dashboard", "", args...)
		if err != nil {
			return dashboardLoadedMsg{err: err}
		}
		if resp.Status == "error" {
			return dashboardLoadedMsg{err: fmt.Errorf("%s", resp.Message)}
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
				// Status format: "idle (active)" or "working (active)"
				e.MPStatus = row[3]
				e.HemStatus = "active"
				if strings.Contains(row[3], "(completed)") {
					e.HemStatus = "completed"
				}
				if idx := strings.Index(row[3], " ("); idx >= 0 {
					e.MPStatus = row[3][:idx]
				}
			}
			if len(row) > 4 {
				e.Moneypenny = row[4]
			}
			if len(row) > 5 {
				e.LastActive = row[5]
			}

			// Determine category from parsed status.
			// 0=READY, 1=WORKING, 2=IDLE, 3=COMPLETED
			e.Category = 1 // WORKING
			if e.HemStatus == "completed" {
				e.Category = 3
			} else if e.MPStatus == "ready" {
				e.Category = 0
			} else if e.MPStatus == "idle" || e.MPStatus == "offline" {
				e.Category = 2
			}

			entries = append(entries, e)
		}

		return dashboardLoadedMsg{entries: entries}
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
	return m.loadDashboard()
}

func (m dashboardModel) Update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case dashboardLoadedMsg:
		m.loading = false
		m.entries = msg.entries
		m.err = msg.err
		if m.cursor >= len(m.entries) {
			m.cursor = max(0, len(m.entries)-1)
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
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "a":
			m.showAll = !m.showAll
			m.loading = true
			return m, m.loadDashboard()
		case "r":
			m.loading = true
			return m, m.loadDashboard()
		}
	}
	return m, nil
}

func (m dashboardModel) selectedEntry() *dashboardEntry {
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

var (
	categoryReadyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Bold(true)
	categoryWorkingStyle = lipgloss.NewStyle().Foreground(colorWarning).Bold(true)
	categoryIdleStyle    = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	categoryDoneStyle    = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)

	statusCompleted = lipgloss.NewStyle().Foreground(colorMuted).Render("✓ done")
)

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

	var b strings.Builder

	if m.title != "" {
		b.WriteString(titleStyle.Render(" "+m.title+" "))
		b.WriteString("\n")
	}

	// Available height for rows.
	maxRows := m.height - 4
	if m.title != "" {
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
	if end > len(m.entries) {
		end = len(m.entries)
	}

	// Show project column if not already filtered by project and any entry has a project.
	showProject := m.projectFilter == ""
	if showProject {
		hasAnyProject := false
		for _, e := range m.entries {
			if e.Project != "" {
				hasAnyProject = true
				break
			}
		}
		showProject = hasAnyProject
	}

	lastCat := -1

	for i := start; i < end; i++ {
		e := m.entries[i]

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
		name := truncate(e.Name, 20)
		if name == "" {
			name = truncate(e.SessionID, 15)
		}
		mp := truncate(e.Moneypenny, 10)
		status := statusBadge(e.MPStatus)
		lastActive := truncate(e.LastActive, 14)

		var line string
		if showProject {
			project := truncate(e.Project, 12)
			if project == "" {
				project = "-"
			}
			line = fmt.Sprintf("  %-22s %-14s %-10s %-12s %s", name, project, status, mp, lastActive)
		} else {
			line = fmt.Sprintf("  %-22s %-10s %-12s %s", name, status, mp, lastActive)
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

	if len(m.entries) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.entries))))
		b.WriteString("\n")
	}

	return b.String()
}
