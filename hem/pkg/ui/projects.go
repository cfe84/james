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

// createProjectModel is a form for creating a new project.
type createProjectModel struct {
	fields   []formField
	cursor   int
	width    int
	height   int
	err      error
	creating bool
	client   *client
}

type projectCreatedMsg struct {
	err error
}

func newCreateProjectModel(c *client) createProjectModel {
	return createProjectModel{
		client: c,
		fields: []formField{
			{label: "Name", flag: "--name", value: ""},
			{label: "Moneypenny", flag: "-m", value: ""},
			{label: "Agent", flag: "--agent", value: ""},
			{label: "Path", flag: "--path", value: ""},
			{label: "System Prompt", flag: "--system-prompt", value: ""},
		},
	}
}

func (m createProjectModel) createProject() tea.Cmd {
	return func() tea.Msg {
		var args []string
		for _, f := range m.fields {
			if f.value == "" {
				continue
			}
			args = append(args, f.flag, f.value)
		}
		err := m.client.createProject(args)
		return projectCreatedMsg{err: err}
	}
}

func (m createProjectModel) Update(msg tea.Msg) (createProjectModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.creating {
			return m, nil
		}
		field := &m.fields[m.cursor]
		switch msg.String() {
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.fields)-1 {
				m.cursor++
			}
		case "tab":
			m.cursor = (m.cursor + 1) % len(m.fields)
		case "enter":
			name := m.fields[0].value
			if strings.TrimSpace(name) != "" {
				m.creating = true
				return m, m.createProject()
			}
		case "backspace":
			if len(field.value) > 0 {
				field.value = field.value[:len(field.value)-1]
			}
		case "ctrl+u":
			field.value = ""
		default:
			if msg.Type == tea.KeyRunes {
				field.value += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				field.value += " "
			}
		}
	}
	return m, nil
}

func (m createProjectModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" New Project "))
	b.WriteString("\n\n")

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")
		var value string
		if i == m.cursor {
			value = fieldActiveStyle.Render(f.value + "█")
		} else {
			if f.value == "" {
				value = fieldInactiveStyle.Render("(empty)")
			} else {
				value = fieldInactiveStyle.Render(f.value)
			}
		}
		b.WriteString("  " + label + " " + value + "\n")
	}

	b.WriteString("\n")
	if m.creating {
		b.WriteString("  Creating project...")
	} else {
		b.WriteString(statusDescStyle.Render(" Enter ") + " submit  " +
			statusDescStyle.Render(" Tab ") + " next field  " +
			statusDescStyle.Render(" Esc ") + " cancel")
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}

// editProjectModel is a form for editing an existing project.
type editProjectModel struct {
	projectID string
	fields    []formField
	original  []string
	cursor    int
	width     int
	height    int
	err       error
	saving    bool
	client    *client
}

type projectUpdatedMsg struct {
	err error
}

func newEditProjectModel(c *client, p *projectInfo) editProjectModel {
	fields := []formField{
		{label: "Name", flag: "--name", value: p.Name},
		{label: "Status", flag: "--status", value: p.Status},
		{label: "Moneypenny", flag: "-m", value: p.Moneypenny},
		{label: "Agent", flag: "--agent", value: p.Agent},
		{label: "Path", flag: "--path", value: p.Paths},
		{label: "System Prompt", flag: "--system-prompt", value: ""},
	}
	original := make([]string, len(fields))
	for i, f := range fields {
		original[i] = f.value
	}
	return editProjectModel{
		client:    c,
		projectID: p.ID,
		fields:    fields,
		original:  original,
	}
}

func (m editProjectModel) save() tea.Cmd {
	return func() tea.Msg {
		fields := make(map[string]string)
		for i, f := range m.fields {
			if f.value != m.original[i] {
				fields[f.flag] = f.value
			}
		}
		if len(fields) == 0 {
			return projectUpdatedMsg{err: nil}
		}
		err := m.client.updateProject(m.projectID, fields)
		return projectUpdatedMsg{err: err}
	}
}

func (m editProjectModel) Update(msg tea.Msg) (editProjectModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.saving {
			return m, nil
		}
		field := &m.fields[m.cursor]
		switch msg.String() {
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down":
			if m.cursor < len(m.fields)-1 {
				m.cursor++
			}
		case "tab":
			m.cursor = (m.cursor + 1) % len(m.fields)
		case "enter":
			m.saving = true
			return m, m.save()
		case "backspace":
			if len(field.value) > 0 {
				field.value = field.value[:len(field.value)-1]
			}
		case "ctrl+u":
			field.value = ""
		default:
			if msg.Type == tea.KeyRunes {
				field.value += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				field.value += " "
			}
		}
	}
	return m, nil
}

func (m editProjectModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(fmt.Sprintf(" Edit Project: %s ", truncate(m.fields[0].value, 20))))
	b.WriteString("\n\n")

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")

		changed := ""
		if f.value != m.original[i] {
			changed = lipgloss.NewStyle().Foreground(colorWarning).Render(" *")
		}

		var value string
		if i == m.cursor {
			value = fieldActiveStyle.Render(f.value + "█")
		} else {
			if f.value == "" {
				value = fieldInactiveStyle.Render("(empty)")
			} else {
				value = fieldInactiveStyle.Render(f.value)
			}
		}
		b.WriteString("  " + label + " " + value + changed + "\n")
	}

	b.WriteString("\n")
	if m.saving {
		b.WriteString("  Saving...")
	} else {
		b.WriteString(statusDescStyle.Render(" Enter ") + " save  " +
			statusDescStyle.Render(" Tab ") + " next field  " +
			statusDescStyle.Render(" Esc ") + " cancel  " +
			statusDescStyle.Render(" Ctrl+U ") + " clear")
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
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
