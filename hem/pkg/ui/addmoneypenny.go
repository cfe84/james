package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type addMoneypennyModel struct {
	fields   []formField
	cursor   int
	width    int
	height   int
	err      error
	creating bool
	client   *client
}

type moneypennyAddedMsg struct{ err error }

func newAddMoneypennyModel(c *client) addMoneypennyModel {
	return addMoneypennyModel{
		client: c,
		fields: []formField{
			{label: "Name", flag: "-n", value: ""},
			{label: "Type", flag: "", value: "local"},       // local, fifo, mi6
			{label: "FIFO Folder", flag: "--fifo-folder", value: ""},
			{label: "MI6 Address", flag: "--mi6", value: ""}, // host/session_id
		},
	}
}

func (m addMoneypennyModel) addMoneypenny() tea.Cmd {
	return func() tea.Msg {
		var args []string
		name := m.fields[0].value
		mpType := m.fields[1].value

		args = append(args, "-n", name)

		switch mpType {
		case "local":
			args = append(args, "--local")
		case "fifo":
			folder := m.fields[2].value
			if folder != "" {
				args = append(args, "--fifo-folder", folder)
			}
		case "mi6":
			addr := m.fields[3].value
			if addr != "" {
				args = append(args, "--mi6", addr)
			}
		}

		err := m.client.addMoneypenny(args)
		return moneypennyAddedMsg{err: err}
	}
}

func (m addMoneypennyModel) Update(msg tea.Msg) (addMoneypennyModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.creating {
			return m, nil
		}
		field := &m.fields[m.cursor]
		switch msg.String() {
		case "up":
			m.cursor = m.prevVisibleField(m.cursor)
		case "down":
			m.cursor = m.nextVisibleField(m.cursor)
		case "tab":
			m.cursor = m.nextVisibleField(m.cursor)
		case "enter":
			name := strings.TrimSpace(m.fields[0].value)
			if name != "" {
				m.creating = true
				return m, m.addMoneypenny()
			}
		case "backspace":
			if m.cursor == 1 {
				// Type field: cycle backwards
				m.fields[1].value = m.prevType()
			} else if len(field.value) > 0 {
				field.value = field.value[:len(field.value)-1]
			}
		case "ctrl+u":
			if m.cursor != 1 {
				field.value = ""
			}
		case " ":
			if m.cursor == 1 {
				m.fields[1].value = m.nextType()
			} else {
				field.value += " "
			}
		default:
			if m.cursor == 1 {
				// Type field ignores text input, use space to cycle
			} else if msg.Type == tea.KeyRunes {
				field.value += string(msg.Runes)
			}
		}
	}
	return m, nil
}

func (m addMoneypennyModel) nextType() string {
	switch m.fields[1].value {
	case "local":
		return "fifo"
	case "fifo":
		return "mi6"
	default:
		return "local"
	}
}

func (m addMoneypennyModel) prevType() string {
	switch m.fields[1].value {
	case "local":
		return "mi6"
	case "mi6":
		return "fifo"
	default:
		return "local"
	}
}

// isFieldVisible returns whether a field should be shown for the current type.
func (m addMoneypennyModel) isFieldVisible(i int) bool {
	if i <= 1 {
		return true // Name and Type always visible
	}
	mpType := m.fields[1].value
	switch i {
	case 2: // FIFO Folder
		return mpType == "fifo"
	case 3: // MI6 Address
		return mpType == "mi6"
	}
	return false
}

func (m addMoneypennyModel) nextVisibleField(from int) int {
	for i := from + 1; i < len(m.fields); i++ {
		if m.isFieldVisible(i) {
			return i
		}
	}
	// Wrap to first visible
	for i := 0; i < len(m.fields); i++ {
		if m.isFieldVisible(i) {
			return i
		}
	}
	return from
}

func (m addMoneypennyModel) prevVisibleField(from int) int {
	for i := from - 1; i >= 0; i-- {
		if m.isFieldVisible(i) {
			return i
		}
	}
	return from
}

func (m addMoneypennyModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" Add Moneypenny "))
	b.WriteString("\n\n")

	for i, f := range m.fields {
		if !m.isFieldVisible(i) {
			continue
		}
		label := labelStyle.Render(f.label + ":")
		var value string
		if i == m.cursor {
			if i == 1 {
				// Type selector
				value = fieldActiveStyle.Render("< " + f.value + " >")
			} else {
				value = fieldActiveStyle.Render(f.value + "\u2588")
			}
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
		b.WriteString("  Adding moneypenny...")
	} else {
		hint := statusDescStyle.Render(" Enter ") + " add  " +
			statusDescStyle.Render(" Tab ") + " next field  "
		if m.cursor == 1 {
			hint += statusDescStyle.Render(" Space ") + " cycle type  "
		}
		hint += statusDescStyle.Render(" Esc ") + " cancel"
		b.WriteString(hint)
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}
