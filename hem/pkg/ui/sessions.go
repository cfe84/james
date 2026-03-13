package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// sessionsModel displays and manages the session list.
type sessionsModel struct {
	sessions []sessionInfo
	cursor   int
	width    int
	height   int
	err      error
	loading  bool
	client   *client
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
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "r":
			m.loading = true
			return m, m.loadSessions()
		}
	}
	return m, nil
}

func (m sessionsModel) selectedSession() *sessionInfo {
	if len(m.sessions) == 0 || m.cursor >= len(m.sessions) {
		return nil
	}
	return &m.sessions[m.cursor]
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

	var b strings.Builder

	// Header
	header := fmt.Sprintf("  %-14s %-20s %-10s %-12s %-14s %-14s",
		"ID", "Name", "Status", "Moneypenny", "Created", "Last Active")
	b.WriteString(sessionHeaderStyle.Render(header))
	b.WriteString("\n")

	// Available height for rows.
	maxRows := m.height - 4 // header + borders + status
	if maxRows < 1 {
		maxRows = 10
	}

	// Compute visible window.
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.sessions) {
		end = len(m.sessions)
	}

	for i := start; i < end; i++ {
		s := m.sessions[i]
		baseStatus := s.Status
		subInfo := ""
		if idx := strings.Index(s.Status, " ["); idx >= 0 {
			baseStatus = s.Status[:idx]
			subInfo = s.Status[idx+1:]
		}
		status := statusBadge(baseStatus)
		if subInfo != "" {
			status += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(subInfo)
		}
		id := truncate(s.SessionID, 12)
		name := truncate(s.Name, 18)
		mp := truncate(s.Moneypenny, 10)
		lastActive := relativeTime(s.LastAccessed)
		if lastActive == "" {
			lastActive = relativeTime(s.CreatedAt)
		}

		line := fmt.Sprintf("  %-14s %-20s %-10s %-12s %s",
			id, name, status, mp, lastActive)

		if i == m.cursor {
			// Pad to full width for highlight
			if m.width > 0 && len(line) < m.width {
				line += strings.Repeat(" ", m.width-lipgloss.Width(line))
			}
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	if len(m.sessions) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.sessions))))
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
