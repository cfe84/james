package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// editModel is a form for editing an existing session's parameters.
type editModel struct {
	sessionID string
	fields    []formField
	original  []string // original values to detect changes
	cursor    int
	width     int
	height    int
	err       error
	saving    bool
	loading   bool
	client    *client
}

type sessionUpdatedMsg struct {
	err error
}

type sessionDetailLoadedMsg struct {
	detail *sessionDetail
	err    error
}

func newEditModel(c *client, sessionID string) editModel {
	return editModel{
		client:    c,
		sessionID: sessionID,
		loading:   true,
		fields: []formField{
			{label: "Name", flag: "--name", value: ""},
			{label: "Project", flag: "--project", value: ""},
			{label: "System Prompt", flag: "--system-prompt", value: ""},
			{label: "Path", flag: "--path", value: ""},
			{label: "Yolo", flag: "--yolo", isBool: true, value: "true"},
		},
	}
}

func (m editModel) loadDetail() tea.Cmd {
	return func() tea.Msg {
		detail, err := m.client.showSession(m.sessionID)
		return sessionDetailLoadedMsg{detail: detail, err: err}
	}
}

func (m editModel) save() tea.Cmd {
	return func() tea.Msg {
		fields := make(map[string]string)
		for i, f := range m.fields {
			if f.value != m.original[i] {
				fields[f.flag] = f.value
			}
		}
		if len(fields) == 0 {
			return sessionUpdatedMsg{err: nil}
		}
		err := m.client.updateSession(m.sessionID, fields)
		return sessionUpdatedMsg{err: err}
	}
}

func (m editModel) Update(msg tea.Msg) (editModel, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionDetailLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		d := msg.detail
		m.fields[0].value = d.Name
		// Project field (index 1) is left empty — it's a local hem concept,
		// not available from moneypenny's session detail.
		m.fields[2].value = d.SystemPrompt
		m.fields[3].value = d.Path
		if d.Yolo {
			m.fields[4].value = "true"
		} else {
			m.fields[4].value = "false"
		}
		// Store originals for diff.
		m.original = make([]string, len(m.fields))
		for i, f := range m.fields {
			m.original[i] = f.value
		}

	case tea.KeyMsg:
		if m.saving || m.loading {
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

func (m editModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(fmt.Sprintf(" Edit Session: %s ", truncate(m.sessionID, 20))))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString("  Loading session details...")
		return dialogStyle.Render(b.String())
	}

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")

		// Show change indicator.
		changed := ""
		if m.original != nil && f.value != m.original[i] {
			changed = lipgloss.NewStyle().Foreground(colorWarning).Render(" *")
		}

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
