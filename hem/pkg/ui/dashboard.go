package ui

import (
	"encoding/json"
	"fmt"
	"sort"
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
	SessionID  string
	Name       string
	Project    string
	MPStatus   string // ready/idle/working/offline
	HemStatus  string // active/completed
	SubInfo    string // e.g. "[3 subs, 1 ready]"
	Moneypenny string
	CreatedAt  string
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

		// Sort within each category by project, then name.
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Category != entries[j].Category {
				return entries[i].Category < entries[j].Category
			}
			if entries[i].Project != entries[j].Project {
				return entries[i].Project < entries[j].Project
			}
			return entries[i].Name < entries[j].Name
		})

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

	// Calculate column widths based on terminal width.
	// Fixed columns: indent(2) + status(10) + created(14) + lastActive(14) + spacing(~10)
	// Flexible: name, moneypenny, project
	w := m.width
	if w < 80 {
		w = 80
	}
	fixedWidth := 2 + 10 + 14 + 14 + 10 // indent + status + created + lastActive + gaps
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
		name := truncate(e.Name, nameWidth)
		if name == "" {
			name = truncate(e.SessionID, nameWidth)
		}
		mp := truncate(e.Moneypenny, mpWidth)
		status := statusBadge(e.MPStatus)
		if e.SubInfo != "" {
			status += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(e.SubInfo)
		}
		created := truncate(e.CreatedAt, 14)
		lastActive := truncate(e.LastActive, 14)

		nameFmt := fmt.Sprintf("%%-%ds", nameWidth+2)
		mpFmt := fmt.Sprintf("%%-%ds", mpWidth+2)

		var line string
		if showProject {
			project := truncate(e.Project, projWidth)
			if project == "" {
				project = "-"
			}
			projFmt := fmt.Sprintf("%%-%ds", projWidth+2)
			line = fmt.Sprintf("  "+nameFmt+projFmt+"%-10s "+mpFmt+"%-14s %s", name, project, status, mp, created, lastActive)
		} else {
			line = fmt.Sprintf("  "+nameFmt+"%-10s "+mpFmt+"%-14s %s", name, status, mp, created, lastActive)
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
