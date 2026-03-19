package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sessionsModel displays and manages the session list.
type sessionsModel struct {
	sessions   []sessionInfo
	cursor     int
	width      int
	height     int
	err        error
	loading    bool
	client     *client
	filtering  bool   // true when filter input is active
	filterText string // current filter text (case insensitive match on name)
}

func newSessionsModel(c *client) sessionsModel {
	return sessionsModel{
		client:  c,
		loading: true,
	}
}

// Messages
type sessionsLoadedMsg struct {
	sessions []sessionInfo
	err      error
}

type sessionDeletedMsg struct{ err error }
type sessionStoppedMsg struct{ err error }

func (m sessionsModel) loadSessions() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.client.listSessions("")
		return sessionsLoadedMsg{sessions: sessions, err: err}
	}
}

func (m sessionsModel) deleteSession(id string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteSession(id)
		return sessionDeletedMsg{err: err}
	}
}

func (m sessionsModel) stopSession(id string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.stopSession(id)
		return sessionStoppedMsg{err: err}
	}
}

func (m sessionsModel) Init() tea.Cmd {
	return m.loadSessions()
}

func (m sessionsModel) Update(msg tea.Msg) (sessionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionsLoadedMsg:
		m.loading = false
		m.sessions = msg.sessions
		m.err = msg.err
		if m.cursor >= len(m.sessions) {
			m.cursor = max(0, len(m.sessions)-1)
		}

	case sessionDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadSessions()

	case sessionStoppedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadSessions()

	case tea.KeyMsg:
		// Filter input mode.
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filterText = ""
				m.cursor = 0
			case "enter":
				m.filtering = false
				m.cursor = 0
			case "backspace":
				if len(m.filterText) > 0 {
					m.filterText = m.filterText[:len(m.filterText)-1]
					m.cursor = 0
				}
			case "ctrl+u":
				m.filterText = ""
				m.cursor = 0
			default:
				if msg.Type == tea.KeyRunes {
					m.filterText += string(msg.Runes)
					m.cursor = 0
				} else if msg.String() == " " {
					m.filterText += " "
					m.cursor = 0
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			filtered := m.filteredSessions()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
		case "/":
			m.filtering = true
			m.filterText = ""
			m.cursor = 0
		case "r":
			m.loading = true
			return m, m.loadSessions()
		}
	}
	return m, nil
}

// filteredSessions returns sessions matching the current filter text.
func (m sessionsModel) filteredSessions() []sessionInfo {
	if m.filterText == "" {
		return m.sessions
	}
	ft := strings.ToLower(m.filterText)
	var result []sessionInfo
	for _, s := range m.sessions {
		if strings.Contains(strings.ToLower(s.Name), ft) {
			result = append(result, s)
		}
	}
	return result
}

func (m sessionsModel) selectedSession() *sessionInfo {
	filtered := m.filteredSessions()
	if len(filtered) == 0 || m.cursor >= len(filtered) {
		return nil
	}
	return &filtered[m.cursor]
}

func (m sessionsModel) View() string {
	if m.loading {
		return "\n  Loading sessions..."
	}
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v", m.err)
	}
	if len(m.sessions) == 0 {
		return "\n  No sessions. Press [n] to create one."
	}

	sessions := m.filteredSessions()

	var b strings.Builder

	// Filter bar.
	if m.filtering {
		b.WriteString("  " + labelStyle.Render("Filter:") + " " + fieldActiveStyle.Render(m.filterText+"█"))
		b.WriteString("\n")
	} else if m.filterText != "" {
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("filter: %q (%d matches, / to edit, esc to clear)", m.filterText, len(sessions))))
		b.WriteString("\n")
	}

	if len(sessions) == 0 {
		b.WriteString("\n  No matching sessions.")
		return b.String()
	}

	// Header
	header := fmt.Sprintf("  %-14s %-20s %-10s %-12s %s",
		"ID", "Name", "Status", "Moneypenny", "Last Active")
	b.WriteString(sessionHeaderStyle.Render(header))
	b.WriteString("\n")

	// Available height for rows.
	maxRows := m.height - 4 // header + borders + status
	if m.filtering || m.filterText != "" {
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
	if end > len(sessions) {
		end = len(sessions)
	}

	for i := start; i < end; i++ {
		s := sessions[i]
		baseStatus := s.Status
		subInfo := ""
		if idx := strings.Index(s.Status, " ["); idx >= 0 {
			baseStatus = s.Status[:idx]
			subInfo = s.Status[idx+1:]
		}
		id := truncate(s.SessionID, 12)
		name := truncate(s.Name, 18)
		mp := truncate(s.Moneypenny, 10)
		lastActive := relativeTime(s.LastAccessed)
		if lastActive == "" {
			lastActive = "-"
		}

		selected := i == m.cursor
		if selected {
			statusText := statusPlain(baseStatus)
			if subInfo != "" {
				statusText += " " + subInfo
			}
			line := fmt.Sprintf("  %-14s %-20s %-10s %-12s %s",
				id, name, statusText, mp, lastActive)
			style := sessionSelectedStyle
			if m.width > 0 {
				style = style.Width(m.width)
			}
			b.WriteString(style.Render(line))
		} else {
			status := statusBadge(baseStatus)
			if subInfo != "" {
				status += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(subInfo)
			}
			line := fmt.Sprintf("  %-14s %-20s %s %-12s %s",
				id, name, padRight(status, 10), mp, lastActive)
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	if len(sessions) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(sessions))))
		b.WriteString("\n")
	}

	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
