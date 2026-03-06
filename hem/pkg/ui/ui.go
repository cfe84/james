package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	viewSessions view = iota
	viewChat
	viewCreate
	viewEdit
)

// Model is the top-level bubbletea model.
type Model struct {
	currentView view
	sessions    sessionsModel
	chat        chatModel
	create      createModel
	edit        editModel
	width       int
	height      int
	client      *client
	statusMsg   string
}

// New creates the initial UI model.
func New() Model {
	c := newClient()
	return Model{
		currentView: viewSessions,
		sessions:    newSessionsModel(c),
		client:      c,
	}
}

func (m Model) Init() tea.Cmd {
	return m.sessions.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.sessions.width = msg.Width
		m.sessions.height = msg.Height - 3 // status bar
		m.chat.width = msg.Width
		m.chat.height = msg.Height - 3
		m.create.width = msg.Width
		m.create.height = msg.Height - 3
		m.edit.width = msg.Width
		m.edit.height = msg.Height - 3
		return m, nil

	case tea.KeyMsg:
		// Global keys.
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.currentView == viewSessions {
				return m, tea.Quit
			}
		case "esc":
			if m.currentView != viewSessions {
				m.currentView = viewSessions
				m.statusMsg = ""
				// Refresh session list.
				m.sessions.loading = true
				return m, m.sessions.loadSessions()
			}
		}

		// View-specific keys.
		switch m.currentView {
		case viewSessions:
			return m.updateSessions(msg)
		case viewChat:
			return m.updateChat(msg)
		case viewCreate:
			return m.updateCreate(msg)
		case viewEdit:
			return m.updateEdit(msg)
		}

	// Route messages to appropriate view.
	case sessionsLoadedMsg, sessionDeletedMsg, sessionStoppedMsg:
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		return m, cmd

	case historyLoadedMsg, messageSentMsg:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd

	case sessionDetailLoadedMsg:
		var cmd tea.Cmd
		m.edit, cmd = m.edit.Update(msg)
		return m, cmd

	case sessionUpdatedMsg:
		um := msg
		if um.err != nil {
			m.edit.err = um.err
			m.edit.saving = false
			return m, nil
		}
		m.statusMsg = "Session updated"
		m.currentView = viewSessions
		m.sessions.loading = true
		return m, m.sessions.loadSessions()

	case sessionCreatedMsg:
		cm := msg
		if cm.err != nil {
			m.create.err = cm.err
			m.create.creating = false
			return m, nil
		}
		// Switch to chat with the new session.
		m.statusMsg = fmt.Sprintf("Session %s created", truncate(cm.sessionID, 12))
		m.chat = newChatModel(m.client, cm.sessionID, "")
		m.chat.width = m.width
		m.chat.height = m.height - 3
		// Add the response to conversation.
		if cm.response != "" {
			m.chat.conversation = []conversationTurn{
				{Role: "user", Content: m.create.fields[0].value},
				{Role: "assistant", Content: cm.response},
			}
			m.chat.loading = false
		}
		m.currentView = viewChat
		if cm.response == "" {
			return m, m.chat.loadHistory()
		}
		return m, nil
	}

	return m, nil
}

func (m Model) updateSessions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		s := m.sessions.selectedSession()
		if s != nil {
			m.chat = newChatModel(m.client, s.SessionID, s.Name)
			m.chat.width = m.width
			m.chat.height = m.height - 3
			m.currentView = viewChat
			return m, m.chat.loadHistory()
		}
	case "n":
		m.create = newCreateModel(m.client)
		m.create.width = m.width
		m.create.height = m.height - 3
		m.currentView = viewCreate
		return m, nil
	case "e":
		s := m.sessions.selectedSession()
		if s != nil {
			m.edit = newEditModel(m.client, s.SessionID)
			m.edit.width = m.width
			m.edit.height = m.height - 3
			m.currentView = viewEdit
			return m, m.edit.loadDetail()
		}
	case "d":
		s := m.sessions.selectedSession()
		if s != nil {
			return m, m.sessions.deleteSession(s.SessionID)
		}
	case "s":
		s := m.sessions.selectedSession()
		if s != nil && s.Status == "working" {
			return m, m.sessions.stopSession(s.SessionID)
		}
	default:
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.chat, cmd = m.chat.Update(msg)
	return m, cmd
}

func (m Model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.create, cmd = m.create.Update(msg)
	return m, cmd
}

func (m Model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.edit, cmd = m.edit.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	var content string
	switch m.currentView {
	case viewSessions:
		content = m.sessions.View()
	case viewChat:
		content = m.chat.View()
	case viewCreate:
		content = m.create.View()
	case viewEdit:
		content = m.edit.View()
	}

	// Status bar.
	statusBar := m.renderStatusBar()

	return content + "\n" + statusBar
}

func (m Model) renderStatusBar() string {
	var keys []string
	switch m.currentView {
	case viewSessions:
		keys = []string{
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("s") + statusDescStyle.Render(" stop"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("q") + statusDescStyle.Render(" quit"),
		}
	case viewChat:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" send"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			statusKeyStyle.Render("pgup/dn") + statusDescStyle.Render(" scroll"),
		}
	case viewCreate:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" create"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewEdit:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" save"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("^U") + statusDescStyle.Render(" clear"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	}

	left := lipgloss.JoinHorizontal(lipgloss.Left, keys...)
	right := ""
	if m.statusMsg != "" {
		right = statusDescStyle.Render(m.statusMsg)
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	padding := statusBarStyle.Render(fmt.Sprintf("%*s", gap, ""))

	return left + padding + right
}

// Run starts the TUI.
func Run() error {
	p := tea.NewProgram(New(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
