package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// memoryModel is a view for viewing and editing session memory.
type memoryModel struct {
	sessionID string
	client    *client
	content   string // current memory content (as loaded)
	input     textInput
	width     int
	height    int
	loading   bool
	saving    bool
	err       error
	dirty     bool // true when input differs from loaded content
}

type memoryLoadedMsg struct {
	content string
	err     error
}

type memorySavedMsg struct {
	err error
}

func newMemoryModel(c *client, sessionID string) memoryModel {
	input := newTextInput(true) // multiline
	return memoryModel{
		client:    c,
		sessionID: sessionID,
		input:     input,
		loading:   true,
	}
}

func (m memoryModel) loadMemory() tea.Cmd {
	return func() tea.Msg {
		content, err := m.client.getMemory(m.sessionID)
		return memoryLoadedMsg{content: content, err: err}
	}
}

func (m memoryModel) saveMemory() tea.Cmd {
	content := m.input.Value()
	return func() tea.Msg {
		err := m.client.updateMemory(m.sessionID, content)
		return memorySavedMsg{err: err}
	}
}

// visibleHeight returns the number of text lines visible in the editor box.
func (m memoryModel) visibleHeight() int {
	h := m.height - 6 // account for title, hints, padding
	if h < 3 {
		h = 3
	}
	return h
}

// pageStep returns the number of lines ctrl+u/ctrl+d move the cursor (half a page).
func (m memoryModel) pageStep() int {
	step := m.visibleHeight() / 2
	if step < 1 {
		step = 1
	}
	return step
}

func (m memoryModel) Update(msg tea.Msg) (memoryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case memoryLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if msg.content == "(empty)" {
			m.content = ""
		} else {
			m.content = msg.content
		}
		m.input.SetValue(m.content)
		m.dirty = false
		return m, nil

	case memorySavedMsg:
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.content = m.input.Value()
		m.dirty = false
		return m, nil

	case tea.KeyMsg:
		if m.loading || m.saving {
			return m, nil
		}

		switch msg.String() {
		case "ctrl+s":
			if m.dirty {
				m.saving = true
				m.err = nil
				return m, m.saveMemory()
			}
			return m, nil
		case "up":
			m.input.MoveCursorVertical(-1)
			return m, nil
		case "down":
			m.input.MoveCursorVertical(1)
			return m, nil
		case "ctrl+u", "pgup":
			m.input.MoveCursorVertical(-m.pageStep())
			return m, nil
		case "ctrl+d", "pgdown":
			m.input.MoveCursorVertical(m.pageStep())
			return m, nil
		}

		// Pass keys to text input.
		m.input.HandleKey(msg)
		m.dirty = m.input.Value() != m.content
		return m, nil
	}

	return m, nil
}

func (m memoryModel) View() string {
	if m.loading {
		return "\n  Loading memory..."
	}

	var b strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	b.WriteString(titleStyle.Render(" Session Memory"))
	b.WriteString("\n")

	if m.err != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
		b.WriteString(errStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
		b.WriteString("\n")
	}

	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	b.WriteString(hintStyle.Render("  Ctrl+S save · ↑/↓ move · Ctrl+U/Ctrl+D page · Esc back"))
	if m.dirty {
		modifiedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
		b.WriteString(modifiedStyle.Render(" [modified]"))
	}
	if m.saving {
		b.WriteString(hintStyle.Render(" saving..."))
	}
	b.WriteString("\n\n")

	// Render the text input area with cursor.
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(m.width - 4)

	inputHeight := m.visibleHeight()

	var rendered string
	if m.input.IsEmpty() {
		rendered = hintStyle.Render("(empty — type to add memory, or let agents update it via CLI)")
	} else {
		rendered = m.input.Render()
	}

	// Scroll the viewport to follow the cursor: show a window of inputHeight
	// lines that always contains the cursor's line.
	lines := strings.Split(rendered, "\n")
	if len(lines) > inputHeight {
		cursorLine := m.input.CursorLine()
		start := 0
		if cursorLine >= inputHeight {
			start = cursorLine - inputHeight + 1
		}
		if start > len(lines)-inputHeight {
			start = len(lines) - inputHeight
		}
		if start < 0 {
			start = 0
		}
		lines = lines[start : start+inputHeight]
	}
	rendered = strings.Join(lines, "\n")

	b.WriteString(inputStyle.Render(rendered))

	return b.String()
}
