package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// moneypenniesModel displays and manages registered moneypennies.
type moneypenniesModel struct {
	moneypennies []moneypennyInfo
	cursor       int
	width        int
	height       int
	err          error
	loading      bool
	statusMsg    string
	client       *client
}

type moneypenniesLoadedMsg struct {
	moneypennies []moneypennyInfo
	err          error
}

type moneypennyDeletedMsg struct{ err error }
type moneypennyPingedMsg struct {
	message string
	err     error
}
type moneypennyDefaultSetMsg struct{ err error }

func newMoneypenniesModel(c *client) moneypenniesModel {
	return moneypenniesModel{
		client:  c,
		loading: true,
	}
}

func (m moneypenniesModel) loadMoneypennies() tea.Cmd {
	return func() tea.Msg {
		mps, err := m.client.listMoneypennies()
		return moneypenniesLoadedMsg{moneypennies: mps, err: err}
	}
}

func (m moneypenniesModel) deleteMoneypenny(name string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteMoneypenny(name)
		return moneypennyDeletedMsg{err: err}
	}
}

func (m moneypenniesModel) pingMoneypenny(name string) tea.Cmd {
	return func() tea.Msg {
		msg, err := m.client.pingMoneypenny(name)
		return moneypennyPingedMsg{message: msg, err: err}
	}
}

func (m moneypenniesModel) setDefault(name string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.setDefaultMoneypenny(name)
		return moneypennyDefaultSetMsg{err: err}
	}
}

func (m moneypenniesModel) selectedMoneypenny() *moneypennyInfo {
	if len(m.moneypennies) == 0 || m.cursor >= len(m.moneypennies) {
		return nil
	}
	return &m.moneypennies[m.cursor]
}

func (m moneypenniesModel) Update(msg tea.Msg) (moneypenniesModel, tea.Cmd) {
	switch msg := msg.(type) {
	case moneypenniesLoadedMsg:
		m.loading = false
		m.moneypennies = msg.moneypennies
		m.err = msg.err
		if m.cursor >= len(m.moneypennies) {
			m.cursor = max(0, len(m.moneypennies)-1)
		}

	case moneypennyDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.statusMsg = "Moneypenny deleted"
		}
		return m, m.loadMoneypennies()

	case moneypennyPingedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.statusMsg = ""
		} else {
			m.err = nil
			m.statusMsg = msg.message
		}

	case moneypennyDefaultSetMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.statusMsg = "Default updated"
		}
		return m, m.loadMoneypennies()

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.moneypennies)-1 {
				m.cursor++
			}
		case "r":
			m.loading = true
			m.statusMsg = ""
			return m, m.loadMoneypennies()
		}
	}
	return m, nil
}

func (m moneypenniesModel) View() string {
	if m.loading {
		return "\n  Loading moneypennies..."
	}
	if m.err != nil && len(m.moneypennies) == 0 {
		return fmt.Sprintf("\n  Error: %v", m.err)
	}
	if len(m.moneypennies) == 0 {
		return "\n  No moneypennies registered. Press 'n' to add one."
	}

	var b strings.Builder

	b.WriteString(titleStyle.Render(" Moneypennies "))
	b.WriteString("\n\n")

	// Header
	header := fmt.Sprintf("  %-20s %-8s %-40s %s", "Name", "Type", "Address", "Default")
	b.WriteString(sessionHeaderStyle.Render(header))
	b.WriteString("\n")

	maxRows := m.height - 6
	if maxRows < 1 {
		maxRows = 10
	}

	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.moneypennies) {
		end = len(m.moneypennies)
	}

	for i := start; i < end; i++ {
		mp := m.moneypennies[i]
		def := ""
		if mp.IsDefault {
			def = lipgloss.NewStyle().Foreground(colorSuccess).Render("★")
		}
		name := truncate(mp.Name, 18)
		addr := truncate(mp.Address, 38)

		line := fmt.Sprintf("  %-20s %-8s %-40s %s", name, mp.Type, addr, def)

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

	if m.statusMsg != "" {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorSuccess).Render(m.statusMsg))
		b.WriteString("\n")
	}
	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
		b.WriteString("\n")
	}

	return b.String()
}
