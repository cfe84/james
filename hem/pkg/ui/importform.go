package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// importModel is a form for importing a session from a JSONL file or session ID.
type importModel struct {
	fields    []formField
	cursor    int
	width     int
	height    int
	err       error
	importing bool
	client    *client
}

type sessionImportedMsg struct {
	message string
	err     error
}

func newImportModel(c *client) importModel {
	return importModel{
		client: c,
		fields: []formField{
			{label: "File or Session ID", flag: "", value: ""},
			{label: "Name", flag: "--name", value: ""},
			{label: "Project", flag: "--project", value: ""},
			{label: "Path", flag: "--path", value: ""},
		},
	}
}

func (m importModel) importSession() tea.Cmd {
	return func() tea.Msg {
		var args []string
		fileOrID := ""
		for _, f := range m.fields {
			if f.flag == "" {
				fileOrID = f.value
				continue
			}
			if f.value == "" {
				continue
			}
			args = append(args, f.flag, f.value)
		}
		args = append(args, fileOrID)

		resp, err := m.client.send("import", "session", args...)
		if err != nil {
			return sessionImportedMsg{err: err}
		}
		if resp.Status == "error" {
			return sessionImportedMsg{err: fmt.Errorf("%s", resp.Message)}
		}
		var result struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			return sessionImportedMsg{message: "Session imported"}
		}
		return sessionImportedMsg{message: result.Message}
	}
}

func (m importModel) Update(msg tea.Msg) (importModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.importing {
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
			fileOrID := m.fields[0].value
			if strings.TrimSpace(fileOrID) != "" {
				m.importing = true
				return m, m.importSession()
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

func (m importModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" Import Session "))
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
	if m.importing {
		b.WriteString("  Importing session...")
	} else {
		b.WriteString(statusDescStyle.Render(" Enter ") + " import  " +
			statusDescStyle.Render(" Tab ") + " next field  " +
			statusDescStyle.Render(" Esc ") + " cancel")
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}
