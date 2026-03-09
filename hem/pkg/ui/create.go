package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// createModel is a form for creating a new session.
type createModel struct {
	fields      []formField
	cursor      int
	width       int
	height      int
	err         error
	creating    bool
	async       bool // if true, add --async flag
	client      *client
}

type formField struct {
	label    string
	value    string
	flag     string // CLI flag name
	isBool   bool
}

func newCreateModel(c *client) createModel {
	return createModel{
		client: c,
		fields: []formField{
			{label: "Prompt", flag: "", value: ""},
			{label: "Name", flag: "--name", value: ""},
			{label: "Project", flag: "--project", value: ""},
			{label: "Agent", flag: "--agent", value: ""},
			{label: "System Prompt", flag: "--system-prompt", value: ""},
			{label: "Path", flag: "--path", value: ""},
			{label: "License to Kill", flag: "--yolo", isBool: true, value: "true"},
			{label: "Gadgets (James tooling)", flag: "--gadgets", isBool: true, value: "false"},
		},
	}
}

func newCreateModelForProject(c *client, projectName string) createModel {
	m := newCreateModel(c)
	m.async = true
	// Pre-fill the project field.
	for i := range m.fields {
		if m.fields[i].flag == "--project" {
			m.fields[i].value = projectName
			break
		}
	}
	return m
}

type sessionCreatedMsg struct {
	sessionID string
	response  string
	err       error
}

func (m createModel) createSession() tea.Cmd {
	return func() tea.Msg {
		var args []string
		prompt := ""
		for _, f := range m.fields {
			if f.flag == "" {
				prompt = f.value
				continue
			}
			if f.value == "" || (f.isBool && f.value == "false") {
				continue
			}
			if f.isBool {
				args = append(args, f.flag)
			} else {
				args = append(args, f.flag, f.value)
			}
		}
		if m.async {
			args = append(args, "--async")
		}
		args = append(args, prompt)
		id, resp, err := m.client.createSession(args)
		return sessionCreatedMsg{sessionID: id, response: resp, err: err}
	}
}

func (m createModel) Update(msg tea.Msg) (createModel, tea.Cmd) {
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
			// If on last field or prompt is filled, submit.
			prompt := m.fields[0].value
			if strings.TrimSpace(prompt) != "" {
				m.creating = true
				return m, m.createSession()
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

func (m createModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" New Agent "))
	b.WriteString("\n\n")

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")
		var value string
		if i == m.cursor {
			if f.isBool {
				if f.value == "true" {
					value = fieldActiveStyle.Render("[x] " + f.value)
				} else {
					value = fieldActiveStyle.Render("[ ] " + f.value)
				}
			} else {
				value = fieldActiveStyle.Render(f.value + "█")
			}
		} else {
			if f.isBool {
				value = fieldInactiveStyle.Render(f.value)
			} else if f.value == "" {
				value = fieldInactiveStyle.Render("(empty)")
			} else {
				value = fieldInactiveStyle.Render(f.value)
			}
		}
		b.WriteString("  " + label + " " + value + "\n")
	}

	b.WriteString("\n")
	if m.creating {
		b.WriteString("  Creating session...")
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
