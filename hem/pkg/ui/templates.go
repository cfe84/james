package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// Template picker (list + use/delete)
// ---------------------------------------------------------------------------

type templatePickerModel struct {
	client      *client
	projectName string
	templates   []templateInfo
	cursor      int
	loading     bool
	err         error
	width       int
	height      int
}

type templatesLoadedMsg struct {
	templates []templateInfo
	err       error
}

type templateUsedMsg struct {
	sessionID string
	err       error
}

type templateDeletedMsg struct {
	err error
}

func newTemplatePickerModel(c *client, projectName string) templatePickerModel {
	return templatePickerModel{
		client:      c,
		projectName: projectName,
		loading:     true,
	}
}

func (m templatePickerModel) loadTemplates() tea.Cmd {
	return func() tea.Msg {
		templates, err := m.client.listTemplates(m.projectName)
		return templatesLoadedMsg{templates: templates, err: err}
	}
}

func (m templatePickerModel) useTemplate(nameOrID string) tea.Cmd {
	return func() tea.Msg {
		sessionID, err := m.client.useTemplate(nameOrID, m.projectName)
		return templateUsedMsg{sessionID: sessionID, err: err}
	}
}

func (m templatePickerModel) deleteTemplate(nameOrID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteTemplate(nameOrID, m.projectName)
		return templateDeletedMsg{err: err}
	}
}

func (m templatePickerModel) Update(msg tea.Msg) (templatePickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case templatesLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.templates = msg.templates
			m.err = nil
		}
	case templateDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.loading = true
			return m, m.loadTemplates()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.templates)-1 {
				m.cursor++
			}
		}
	}
	return m, nil
}

func (m templatePickerModel) isGlobal() bool {
	return m.projectName == ""
}

func (m templatePickerModel) View() string {
	var b strings.Builder

	title := "Templates"
	if m.projectName != "" {
		title = fmt.Sprintf("Templates: %s", m.projectName)
	}
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(title))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString("  Loading templates...")
		return b.String()
	}
	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(fmt.Sprintf("  Error: %v", m.err)))
		b.WriteString("\n")
	}
	if len(m.templates) == 0 {
		if m.isGlobal() {
			b.WriteString("  No templates defined.")
		} else {
			b.WriteString("  No templates. Press n to create one.")
		}
		return b.String()
	}

	w := m.width
	if w < 40 {
		w = 80
	}

	global := m.isGlobal()
	nameWidth := w * 25 / 100
	if nameWidth < 15 {
		nameWidth = 15
	}
	projWidth := 0
	if global {
		projWidth = w * 15 / 100
		if projWidth < 10 {
			projWidth = 10
		}
	}
	agentWidth := 12
	promptWidth := w - nameWidth - projWidth - agentWidth - 8
	if promptWidth < 20 {
		promptWidth = 20
	}

	for i, t := range m.templates {
		name := truncate(t.Name, nameWidth)
		agent := truncate(t.Agent, agentWidth)
		prompt := truncate(t.Prompt, promptWidth)

		nameFmt := fmt.Sprintf("%%-%ds", nameWidth+2)
		agentFmt := fmt.Sprintf("%%-%ds", agentWidth+2)

		var line string
		if global {
			proj := truncate(t.Project, projWidth)
			projFmt := fmt.Sprintf("%%-%ds", projWidth+2)
			line = fmt.Sprintf("  "+nameFmt+projFmt+agentFmt+"%s", name, proj, agent, prompt)
		} else {
			line = fmt.Sprintf("  "+nameFmt+agentFmt+"%s", name, agent, prompt)
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

	return b.String()
}

func (m templatePickerModel) selectedTemplate() *templateInfo {
	if m.cursor >= 0 && m.cursor < len(m.templates) {
		t := m.templates[m.cursor]
		return &t
	}
	return nil
}

// ---------------------------------------------------------------------------
// Create template form
// ---------------------------------------------------------------------------

type createTemplateModel struct {
	client      *client
	projectName string
	fields      []formField
	cursor      int
	creating    bool
	err         error
	width       int
	height      int
}

type templateCreatedMsg struct {
	err error
}

type templateProjectLoadedMsg struct {
	path string
	err  error
}

func newCreateTemplateModel(c *client, projectName string) createTemplateModel {
	return createTemplateModel{
		client:      c,
		projectName: projectName,
		fields: []formField{
			{label: "Name", flag: "--name", value: ""},
			{label: "Prompt", flag: "--prompt", value: ""},
			{label: "Agent", flag: "--agent", value: "claude"},
			{label: "Path", flag: "--path", value: ""},
			{label: "System Prompt", flag: "--system-prompt", value: ""},
			{label: "Yolo", flag: "--yolo", isBool: true, value: "true"},
		},
	}
}

func (m createTemplateModel) loadProjectPath() tea.Cmd {
	return func() tea.Msg {
		p, err := m.client.showProject(m.projectName)
		if err != nil {
			return templateProjectLoadedMsg{err: err}
		}
		path := ""
		if p.Paths != "" && p.Paths != "[]" {
			// Parse first path from JSON array.
			var paths []string
			if json.Unmarshal([]byte(p.Paths), &paths) == nil && len(paths) > 0 {
				path = paths[0]
			}
		}
		return templateProjectLoadedMsg{path: path}
	}
}

func (m createTemplateModel) submit() tea.Cmd {
	return func() tea.Msg {
		args := []string{"--project", m.projectName}
		for _, f := range m.fields {
			if f.isBool {
				if f.value == "true" {
					args = append(args, f.flag)
				}
				continue
			}
			if f.value == "" {
				continue
			}
			args = append(args, f.flag, f.value)
		}
		err := m.client.createTemplate(args)
		return templateCreatedMsg{err: err}
	}
}

func (m createTemplateModel) Update(msg tea.Msg) (createTemplateModel, tea.Cmd) {
	switch msg := msg.(type) {
	case templateProjectLoadedMsg:
		if msg.err == nil && msg.path != "" {
			for i := range m.fields {
				if m.fields[i].flag == "--path" && m.fields[i].value == "" {
					m.fields[i].value = msg.path
				}
			}
		}
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
				return m, m.submit()
			}
		case "backspace":
			if !field.isBool && len(field.value) > 0 {
				field.value = field.value[:len(field.value)-1]
			}
		case "ctrl+u":
			if !field.isBool {
				field.value = ""
			}
		case " ":
			if field.isBool {
				if field.value == "true" {
					field.value = "false"
				} else {
					field.value = "true"
				}
			} else {
				field.value += " "
			}
		default:
			if !field.isBool {
				if msg.Type == tea.KeyRunes {
					field.value += string(msg.Runes)
				}
			}
		}
	}
	return m, nil
}

func (m createTemplateModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" New Template "))
	b.WriteString("\n\n")

	labelWidth := 16
	wrapWidth := m.width - labelWidth - 6
	if wrapWidth < 30 {
		wrapWidth = 60
	}

	for i, f := range m.fields {
		label := labelStyle.Render(fmt.Sprintf("%-14s:", f.label))
		if i == m.cursor {
			if f.isBool {
				check := "[ ]"
				if f.value == "true" {
					check = "[x]"
				}
				b.WriteString("  " + label + " " + fieldActiveStyle.Render(check) + "\n")
			} else {
				lines := wrapText(f.value+"█", wrapWidth)
				b.WriteString("  " + label + " " + fieldActiveStyle.Render(lines[0]) + "\n")
				indent := strings.Repeat(" ", labelWidth+3)
				for _, line := range lines[1:] {
					b.WriteString(indent + fieldActiveStyle.Render(line) + "\n")
				}
			}
		} else {
			if f.isBool {
				b.WriteString("  " + label + " " + fieldInactiveStyle.Render(f.value) + "\n")
			} else if f.value == "" {
				b.WriteString("  " + label + " " + fieldInactiveStyle.Render("(empty)") + "\n")
			} else {
				lines := wrapText(f.value, wrapWidth)
				b.WriteString("  " + label + " " + fieldInactiveStyle.Render(lines[0]) + "\n")
				indent := strings.Repeat(" ", labelWidth+3)
				for _, line := range lines[1:] {
					b.WriteString(indent + fieldInactiveStyle.Render(line) + "\n")
				}
			}
		}
	}

	b.WriteString("\n")
	if m.creating {
		b.WriteString("  Creating template...")
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}
