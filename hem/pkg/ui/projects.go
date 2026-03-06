package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// projectsModel displays and manages the project list.
type projectsModel struct {
	projects []projectInfo
	cursor   int
	width    int
	height   int
	err      error
	loading  bool
	client   *client
}

type projectsLoadedMsg struct {
	projects []projectInfo
	err      error
}

type projectDeletedMsg struct{ err error }

func newProjectsModel(c *client) projectsModel {
	return projectsModel{
		client:  c,
		loading: true,
	}
}

func (m projectsModel) loadProjects() tea.Cmd {
	return func() tea.Msg {
		projects, err := m.client.listProjects("")
		return projectsLoadedMsg{projects: projects, err: err}
	}
}

func (m projectsModel) deleteProject(nameOrID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteProject(nameOrID)
		return projectDeletedMsg{err: err}
	}
}

func (m projectsModel) Init() tea.Cmd {
	return m.loadProjects()
}

func (m projectsModel) Update(msg tea.Msg) (projectsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case projectsLoadedMsg:
		m.loading = false
		m.projects = msg.projects
		m.err = msg.err
		if m.cursor >= len(m.projects) {
			m.cursor = max(0, len(m.projects)-1)
		}

	case projectDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadProjects()

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.projects)-1 {
				m.cursor++
			}
		case "r":
			m.loading = true
			return m, m.loadProjects()
		}
	}
	return m, nil
}

func (m projectsModel) selectedProject() *projectInfo {
	if len(m.projects) == 0 || m.cursor >= len(m.projects) {
		return nil
	}
	return &m.projects[m.cursor]
}

var (
	projectStatusActive = lipgloss.NewStyle().Foreground(colorSuccess).Render("● active")
	projectStatusPaused = lipgloss.NewStyle().Foreground(colorWarning).Render("◉ paused")
	projectStatusDone   = lipgloss.NewStyle().Foreground(colorMuted).Render("✓ done")
)

func projectStatusBadge(status string) string {
	switch status {
	case "active":
		return projectStatusActive
	case "paused":
		return projectStatusPaused
	case "done":
		return projectStatusDone
	default:
		return status
	}
}

func (m projectsModel) View() string {
	if m.loading {
		return "\n  Loading projects..."
	}
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v", m.err)
	}
	if len(m.projects) == 0 {
		return "\n  No projects. Create one with: hem create project --name NAME"
	}

	var b strings.Builder

	// Header
	header := fmt.Sprintf("  %-20s %-10s %-14s %-10s %-20s",
		"Name", "Status", "Moneypenny", "Agent", "Paths")
	b.WriteString(sessionHeaderStyle.Render(header))
	b.WriteString("\n")

	maxRows := m.height - 4
	if maxRows < 1 {
		maxRows = 10
	}

	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.projects) {
		end = len(m.projects)
	}

	for i := start; i < end; i++ {
		p := m.projects[i]
		status := projectStatusBadge(p.Status)
		name := truncate(p.Name, 18)
		mp := truncate(p.Moneypenny, 12)
		agent := truncate(p.Agent, 8)
		paths := truncate(p.Paths, 18)

		line := fmt.Sprintf("  %-20s %-10s %-14s %-10s %-20s",
			name, status, mp, agent, paths)

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

	if len(m.projects) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.projects))))
		b.WriteString("\n")
	}

	return b.String()
}
