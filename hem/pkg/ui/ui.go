package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	viewDashboard view = iota
	viewProjects
	viewProjectDetail
	viewSessions
	viewChat
	viewCreate
	viewEdit
	viewCreateProject
	viewImport
	viewDiff
	viewEditProject
	viewMoneypennies
	viewShell
	viewWizard
)

// Model is the top-level bubbletea model.
type Model struct {
	currentView   view
	previousView  view // for esc navigation
	dashboard     dashboardModel
	projects      projectsModel
	projectDetail dashboardModel // reuses dashboardModel with project filter
	sessions      sessionsModel
	chat          chatModel
	create        createModel
	edit          editModel
	createProject createProjectModel
	editProject   editProjectModel
	importForm    importModel
	diff          diffModel
	moneypennies  moneypenniesModel
	shell         shellModel
	wizard        wizardModel
	width         int
	height        int
	client        *client
	statusMsg     string
	version       string
}

// New creates the initial UI model.
func New(version string) Model {
	c := newClient()
	return Model{
		currentView: viewDashboard,
		dashboard:   newDashboardModel(c),
		sessions:    newSessionsModel(c),
		client:      c,
		version:     version,
	}
}

func (m Model) Init() tea.Cmd {
	return m.dashboard.Init()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 3 // status bar
		m.dashboard.width = msg.Width
		m.dashboard.height = h
		m.projectDetail.width = msg.Width
		m.projectDetail.height = h
		m.projects.width = msg.Width
		m.projects.height = h
		m.sessions.width = msg.Width
		m.sessions.height = h
		m.chat.width = msg.Width
		m.chat.height = h
		m.create.width = msg.Width
		m.create.height = h
		m.edit.width = msg.Width
		m.edit.height = h
		m.createProject.width = msg.Width
		m.createProject.height = h
		m.editProject.width = msg.Width
		m.editProject.height = h
		m.importForm.width = msg.Width
		m.importForm.height = h
		m.diff.width = msg.Width
		m.diff.height = h
		m.moneypennies.width = msg.Width
		m.moneypennies.height = h
		m.shell.width = msg.Width
		m.shell.height = h
		m.wizard.width = msg.Width
		m.wizard.height = h
		return m, nil

	case tea.KeyMsg:
		// Global keys.
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.currentView == viewDashboard {
				return m, tea.Quit
			}
		case "esc":
			if m.currentView == viewChat {
				if !m.chat.commandMode {
					m.chat.commandMode = true
					return m, nil
				}
				// Second Esc: leave chat.
				m.chat.commandMode = false
				m.chat.confirmDelete = false
				prev := m.previousView
				m.currentView = prev
				m.statusMsg = ""
				switch prev {
				case viewProjectDetail:
					m.projectDetail.loading = true
					return m, m.projectDetail.loadDashboard()
				case viewSessions:
					m.sessions.loading = true
					return m, m.sessions.loadSessions()
				default:
					m.currentView = viewDashboard
					m.dashboard.loading = true
					return m, m.dashboard.loadDashboard()
				}
			}
			return m.handleEsc()
		}

		// View-specific keys.
		switch m.currentView {
		case viewDashboard:
			return m.updateDashboard(msg)
		case viewProjects:
			return m.updateProjects(msg)
		case viewProjectDetail:
			return m.updateProjectDetail(msg)
		case viewSessions:
			return m.updateSessions(msg)
		case viewChat:
			return m.updateChat(msg)
		case viewCreate:
			return m.updateCreate(msg)
		case viewEdit:
			return m.updateEdit(msg)
		case viewCreateProject:
			return m.updateCreateProject(msg)
		case viewImport:
			return m.updateImport(msg)
		case viewDiff:
			return m.updateDiff(msg)
		case viewEditProject:
			return m.updateEditProject(msg)
		case viewMoneypennies:
			return m.updateMoneypennies(msg)
		case viewShell:
			return m.updateShell(msg)
		case viewWizard:
			return m.updateWizard(msg)
		}

	// Route messages to appropriate view.
	case dashboardLoadedMsg:
		if m.currentView == viewProjectDetail {
			var cmd tea.Cmd
			m.projectDetail, cmd = m.projectDetail.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd

	case sessionCompletedMsg, dashboardDeletedMsg:
		if m.currentView == viewProjectDetail {
			var cmd tea.Cmd
			m.projectDetail, cmd = m.projectDetail.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd

	case moneypenniesLoadedMsg, moneypennyDeletedMsg, moneypennyPingedMsg, moneypennyDefaultSetMsg:
		var cmd tea.Cmd
		m.moneypennies, cmd = m.moneypennies.Update(msg)
		return m, cmd

	case wizardMPLoadedMsg, wizardDirLoadedMsg:
		var cmd tea.Cmd
		m.wizard, cmd = m.wizard.Update(msg)
		return m, cmd

	case projectsLoadedMsg, projectDeletedMsg:
		var cmd tea.Cmd
		m.projects, cmd = m.projects.Update(msg)
		return m, cmd

	case sessionsLoadedMsg, sessionDeletedMsg, sessionStoppedMsg:
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		return m, cmd

	case chatSessionStoppedMsg:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		if msg.err == nil {
			m.statusMsg = "Session stopped"
		}
		return m, cmd

	case chatSessionCompletedMsg:
		if msg.err != nil {
			m.chat.err = msg.err
		} else {
			m.statusMsg = "Session completed"
			m.chat.commandMode = false
		}
		return m, nil

	case chatSessionDeletedMsg:
		if msg.err != nil {
			m.chat.err = msg.err
			return m, nil
		}
		m.statusMsg = "Session deleted"
		m.chat.commandMode = false
		prev := m.previousView
		m.currentView = prev
		switch prev {
		case viewProjectDetail:
			m.projectDetail.loading = true
			return m, m.projectDetail.loadDashboard()
		case viewSessions:
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		default:
			m.currentView = viewDashboard
			m.dashboard.loading = true
			return m, m.dashboard.loadDashboard()
		}

	case chatPollTickMsg:
		// Only process poll ticks when chat is active.
		if m.currentView == viewChat {
			var cmd tea.Cmd
			m.chat, cmd = m.chat.Update(msg)
			return m, cmd
		}
		// Discard tick if not in chat view.
		return m, nil

	case historyLoadedMsg, messageSentMsg:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd

	case sessionImportedMsg:
		im := msg
		if im.err != nil {
			m.importForm.err = im.err
			m.importForm.importing = false
			return m, nil
		}
		m.statusMsg = im.message
		m.currentView = viewSessions
		m.sessions = newSessionsModel(m.client)
		m.sessions.width = m.width
		m.sessions.height = m.height - 3
		return m, m.sessions.loadSessions()

	case shellCommandDoneMsg:
		var cmd tea.Cmd
		m.shell, cmd = m.shell.Update(msg)
		return m, cmd

	case diffLoadedMsg:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
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
		if m.previousView == viewChat {
			m.currentView = viewChat
			return m, m.chat.loadHistory()
		}
		m.currentView = viewSessions
		m.sessions.loading = true
		return m, m.sessions.loadSessions()

	case projectUpdatedMsg:
		pm := msg
		if pm.err != nil {
			m.editProject.err = pm.err
			m.editProject.saving = false
			return m, nil
		}
		m.statusMsg = "Project updated"
		m.currentView = viewProjects
		m.projects = newProjectsModel(m.client)
		m.projects.width = m.width
		m.projects.height = m.height - 3
		return m, m.projects.loadProjects()

	case projectCreatedMsg:
		pm := msg
		if pm.err != nil {
			m.createProject.err = pm.err
			m.createProject.creating = false
			return m, nil
		}
		m.statusMsg = "Project created"
		m.currentView = viewProjects
		m.projects = newProjectsModel(m.client)
		m.projects.width = m.width
		m.projects.height = m.height - 3
		return m, m.projects.loadProjects()

	case sessionCreatedMsg:
		cm := msg

		// Determine if this came from wizard or old create form.
		isWizard := m.currentView == viewWizard
		isAsync := (isWizard && m.wizard.async) || (!isWizard && m.create.async)

		if cm.err != nil {
			if isWizard {
				m.wizard.err = cm.err
				m.wizard.creating = false
			} else {
				m.create.err = cm.err
				m.create.creating = false
			}
			return m, nil
		}
		m.statusMsg = fmt.Sprintf("Session %s created", truncate(cm.sessionID, 12))

		// If async, go back to where we came from instead of entering chat.
		if isAsync {
			prev := m.previousView
			m.currentView = prev
			switch prev {
			case viewProjectDetail:
				m.projectDetail.loading = true
				return m, m.projectDetail.loadDashboard()
			case viewSessions:
				m.sessions.loading = true
				return m, m.sessions.loadSessions()
			default:
				m.currentView = viewDashboard
				m.dashboard.loading = true
				return m, m.dashboard.loadDashboard()
			}
		}

		m.chat = newChatModel(m.client, cm.sessionID, "")
		m.chat.width = m.width
		m.chat.height = m.height - 3
		if cm.response != "" {
			var prompt string
			if isWizard {
				prompt = m.wizard.fields[0].value
			} else {
				prompt = m.create.fields[0].value
			}
			m.chat.conversation = []conversationTurn{
				{Role: "user", Content: prompt},
				{Role: "assistant", Content: cm.response},
			}
			m.chat.loading = false
		}
		m.currentView = viewChat
		m.previousView = viewDashboard
		if cm.response == "" {
			return m, m.chat.loadHistory()
		}
		return m, nil
	}

	return m, nil
}

func (m Model) handleEsc() (tea.Model, tea.Cmd) {
	switch m.currentView {
	case viewDashboard:
		// Already at root, do nothing.
		return m, nil
	case viewProjects:
		m.currentView = viewDashboard
		m.statusMsg = ""
		m.dashboard.loading = true
		return m, m.dashboard.loadDashboard()
	case viewProjectDetail:
		m.currentView = viewProjects
		m.statusMsg = ""
		m.projects.loading = true
		return m, m.projects.loadProjects()
	case viewSessions:
		m.currentView = viewDashboard
		m.statusMsg = ""
		m.dashboard.loading = true
		return m, m.dashboard.loadDashboard()
	case viewCreate:
		prev := m.previousView
		m.currentView = prev
		m.statusMsg = ""
		switch prev {
		case viewProjectDetail:
			m.projectDetail.loading = true
			return m, m.projectDetail.loadDashboard()
		case viewSessions:
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		default:
			m.dashboard.loading = true
			return m, m.dashboard.loadDashboard()
		}
	case viewEdit:
		prev := m.previousView
		m.currentView = prev
		m.statusMsg = ""
		switch prev {
		case viewChat:
			return m, nil
		case viewSessions:
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		default:
			m.currentView = viewSessions
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		}
	case viewCreateProject, viewEditProject:
		m.currentView = viewProjects
		m.statusMsg = ""
		m.projects.loading = true
		return m, m.projects.loadProjects()
	case viewMoneypennies:
		m.currentView = viewDashboard
		m.statusMsg = ""
		m.dashboard.loading = true
		return m, m.dashboard.loadDashboard()
	case viewImport:
		m.currentView = viewSessions
		m.statusMsg = ""
		m.sessions.loading = true
		return m, m.sessions.loadSessions()
	case viewDiff:
		prev := m.previousView
		m.currentView = prev
		m.statusMsg = ""
		switch prev {
		case viewProjectDetail:
			m.projectDetail.loading = true
			return m, m.projectDetail.loadDashboard()
		case viewSessions:
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		default:
			m.dashboard.loading = true
			return m, m.dashboard.loadDashboard()
		}
	case viewWizard:
		// Delegate to updateWizard which handles step-based back navigation.
		return m.updateWizard(tea.KeyMsg{Type: tea.KeyEscape})
	case viewShell:
		prev := m.previousView
		m.currentView = prev
		m.statusMsg = ""
		switch prev {
		case viewProjectDetail:
			m.projectDetail.loading = true
			return m, m.projectDetail.loadDashboard()
		case viewSessions:
			m.sessions.loading = true
			return m, m.sessions.loadSessions()
		case viewMoneypennies:
			m.moneypennies.loading = true
			return m, m.moneypennies.loadMoneypennies()
		case viewChat:
			return m, nil
		default:
			m.dashboard.loading = true
			return m, m.dashboard.loadDashboard()
		}
	}
	return m, nil
}

func (m Model) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.chat = newChatModel(m.client, e.SessionID, e.Name)
			m.chat.width = m.width
			m.chat.height = m.height - 3
			m.currentView = viewChat
			m.previousView = viewDashboard
			return m, m.chat.loadHistory()
		}
	case "c":
		e := m.dashboard.selectedEntry()
		if e != nil && e.HemStatus != "completed" {
			return m, m.dashboard.completeSession(e.SessionID)
		}
	case "d":
		e := m.dashboard.selectedEntry()
		if e != nil {
			return m, m.dashboard.deleteSession(e.SessionID)
		}
	case "n":
		m.wizard = newWizardModel(m.client)
		m.wizard.width = m.width
		m.wizard.height = m.height - 3
		m.currentView = viewWizard
		m.previousView = viewDashboard
		return m, m.wizard.Init()
	case "e":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.edit = newEditModel(m.client, e.SessionID)
			m.edit.width = m.width
			m.edit.height = m.height - 3
			m.currentView = viewEdit
			return m, m.edit.loadDetail()
		}
	case "g":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.diff = newDiffModel(m.client, e.SessionID)
			m.diff.width = m.width
			m.diff.height = m.height - 3
			m.currentView = viewDiff
			m.previousView = viewDashboard
			return m, m.diff.loadDiff()
		}
	case "x":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.shell = newShellModelFromSession(m.client, e.SessionID, e.Name)
			m.shell.width = m.width
			m.shell.height = m.height - 3
			m.currentView = viewShell
			m.previousView = viewDashboard
			return m, nil
		}
	case "l":
		m.sessions = newSessionsModel(m.client)
		m.sessions.width = m.width
		m.sessions.height = m.height - 3
		m.currentView = viewSessions
		return m, m.sessions.loadSessions()
	case "m":
		m.moneypennies = newMoneypenniesModel(m.client)
		m.moneypennies.width = m.width
		m.moneypennies.height = m.height - 3
		m.currentView = viewMoneypennies
		return m, m.moneypennies.loadMoneypennies()
	case "p":
		m.projects = newProjectsModel(m.client)
		m.projects.width = m.width
		m.projects.height = m.height - 3
		m.currentView = viewProjects
		return m, m.projects.loadProjects()
	default:
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateProjects(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		p := m.projects.selectedProject()
		if p != nil {
			m.projectDetail = newDashboardModel(m.client)
			m.projectDetail.projectFilter = p.Name
			m.projectDetail.title = p.Name
			m.projectDetail.width = m.width
			m.projectDetail.height = m.height - 3
			m.projectDetail.loading = true
			m.currentView = viewProjectDetail
			return m, m.projectDetail.loadDashboard()
		}
	case "d":
		p := m.projects.selectedProject()
		if p != nil {
			return m, m.projects.deleteProject(p.ID)
		}
	case "e":
		p := m.projects.selectedProject()
		if p != nil {
			m.editProject = newEditProjectModel(m.client, p)
			m.editProject.width = m.width
			m.editProject.height = m.height - 3
			m.currentView = viewEditProject
			return m, nil
		}
	case "n":
		m.createProject = newCreateProjectModel(m.client)
		m.createProject.width = m.width
		m.createProject.height = m.height - 3
		m.currentView = viewCreateProject
		return m, nil
	default:
		var cmd tea.Cmd
		m.projects, cmd = m.projects.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateProjectDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.chat = newChatModel(m.client, e.SessionID, e.Name)
			m.chat.width = m.width
			m.chat.height = m.height - 3
			m.currentView = viewChat
			m.previousView = viewProjectDetail
			return m, m.chat.loadHistory()
		}
	case "c":
		e := m.projectDetail.selectedEntry()
		if e != nil && e.HemStatus != "completed" {
			return m, m.projectDetail.completeSession(e.SessionID)
		}
	case "d":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			return m, m.projectDetail.deleteSession(e.SessionID)
		}
	case "e":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.edit = newEditModel(m.client, e.SessionID)
			m.edit.width = m.width
			m.edit.height = m.height - 3
			m.currentView = viewEdit
			return m, m.edit.loadDetail()
		}
	case "g":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.diff = newDiffModel(m.client, e.SessionID)
			m.diff.width = m.width
			m.diff.height = m.height - 3
			m.currentView = viewDiff
			m.previousView = viewProjectDetail
			return m, m.diff.loadDiff()
		}
	case "n":
		m.wizard = newWizardModelForProject(m.client, m.projectDetail.projectFilter)
		m.wizard.width = m.width
		m.wizard.height = m.height - 3
		m.currentView = viewWizard
		m.previousView = viewProjectDetail
		return m, m.wizard.Init()
	case "x":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.shell = newShellModelFromSession(m.client, e.SessionID, e.Name)
			m.shell.width = m.width
			m.shell.height = m.height - 3
			m.currentView = viewShell
			m.previousView = viewProjectDetail
			return m, nil
		}
	default:
		var cmd tea.Cmd
		m.projectDetail, cmd = m.projectDetail.Update(msg)
		return m, cmd
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
			m.previousView = viewSessions
			return m, m.chat.loadHistory()
		}
	case "n":
		m.wizard = newWizardModel(m.client)
		m.wizard.width = m.width
		m.wizard.height = m.height - 3
		m.currentView = viewWizard
		m.previousView = viewSessions
		return m, m.wizard.Init()
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
	case "i":
		m.importForm = newImportModel(m.client)
		m.importForm.width = m.width
		m.importForm.height = m.height - 3
		m.currentView = viewImport
		return m, nil
	case "g":
		s := m.sessions.selectedSession()
		if s != nil {
			m.diff = newDiffModel(m.client, s.SessionID)
			m.diff.width = m.width
			m.diff.height = m.height - 3
			m.currentView = viewDiff
			m.previousView = viewSessions
			return m, m.diff.loadDiff()
		}
	case "x":
		s := m.sessions.selectedSession()
		if s != nil {
			m.shell = newShellModelFromSession(m.client, s.SessionID, s.Name)
			m.shell.width = m.width
			m.shell.height = m.height - 3
			m.currentView = viewShell
			m.previousView = viewSessions
			return m, nil
		}
	default:
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateChat(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.chat.commandMode {
		switch msg.String() {
		case "s":
			m.chat.confirmDelete = false
			return m, func() tea.Msg {
				err := m.chat.client.stopSession(m.chat.sessionID)
				return chatSessionStoppedMsg{err: err}
			}
		case "c":
			m.chat.confirmDelete = false
			return m, func() tea.Msg {
				_, err := m.chat.client.send("complete", "session", m.chat.sessionID)
				if err != nil {
					return chatSessionCompletedMsg{err: err}
				}
				return chatSessionCompletedMsg{}
			}
		case "d":
			if !m.chat.confirmDelete {
				m.chat.confirmDelete = true
				return m, nil
			}
			// Confirmed — delete.
			m.chat.confirmDelete = false
			return m, func() tea.Msg {
				err := m.chat.client.deleteSession(m.chat.sessionID)
				return chatSessionDeletedMsg{err: err}
			}
		case "e":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.edit = newEditModel(m.client, m.chat.sessionID)
			m.edit.width = m.width
			m.edit.height = m.height - 3
			m.currentView = viewEdit
			m.previousView = viewChat
			return m, m.edit.loadDetail()
		case "g":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.diff = newDiffModel(m.client, m.chat.sessionID)
			m.diff.width = m.width
			m.diff.height = m.height - 3
			m.currentView = viewDiff
			m.previousView = viewChat
			return m, m.diff.loadDiff()
		case "x":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.shell = newShellModelFromSession(m.client, m.chat.sessionID, m.chat.sessionName)
			m.shell.width = m.width
			m.shell.height = m.height - 3
			m.currentView = viewShell
			m.previousView = viewChat
			return m, nil
		case "t":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.chat.scheduling = true
			m.chat.scheduleAt = ""
			m.chat.input = ""
			m.chat.cursorPos = 0
			return m, nil
		default:
			m.chat.confirmDelete = false
		}
	}
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

func (m Model) updateCreateProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.createProject, cmd = m.createProject.Update(msg)
	return m, cmd
}

func (m Model) updateImport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.importForm, cmd = m.importForm.Update(msg)
	return m, cmd
}

func (m Model) updateMoneypennies(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			m.moneypennies.statusMsg = "Pinging..."
			m.moneypennies.err = nil
			return m, m.moneypennies.pingMoneypenny(mp.Name)
		}
	case "d":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			return m, m.moneypennies.deleteMoneypenny(mp.Name)
		}
	case "s":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			return m, m.moneypennies.setDefault(mp.Name)
		}
	case "x":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			m.shell = newShellModel(m.client, mp.Name, "", "Shell: "+mp.Name)
			m.shell.width = m.width
			m.shell.height = m.height - 3
			m.currentView = viewShell
			m.previousView = viewMoneypennies
			return m, nil
		}
	default:
		var cmd tea.Cmd
		m.moneypennies, cmd = m.moneypennies.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateEditProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.editProject, cmd = m.editProject.Update(msg)
	return m, cmd
}

func (m Model) updateDiff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.diff, cmd = m.diff.Update(msg)
	return m, cmd
}

func (m Model) updateShell(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.shell, cmd = m.shell.Update(msg)
	return m, cmd
}

func (m Model) updateWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		switch m.wizard.step {
		case wizardStepMoneypenny:
			// Leave wizard entirely.
			m.currentView = m.previousView
			switch m.previousView {
			case viewDashboard:
				m.dashboard.loading = true
				return m, m.dashboard.loadDashboard()
			case viewProjectDetail:
				m.projectDetail.loading = true
				return m, m.projectDetail.loadDashboard()
			case viewSessions:
				m.sessions.loading = true
				return m, m.sessions.loadSessions()
			default:
				m.currentView = viewDashboard
				m.dashboard.loading = true
				return m, m.dashboard.loadDashboard()
			}
		case wizardStepPath:
			m.wizard.step = wizardStepMoneypenny
			m.wizard.err = nil
			return m, nil
		case wizardStepForm:
			m.wizard.step = wizardStepPath
			m.wizard.err = nil
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.wizard, cmd = m.wizard.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	var content string
	switch m.currentView {
	case viewDashboard:
		content = m.dashboard.View()
	case viewProjects:
		content = m.projects.View()
	case viewProjectDetail:
		content = m.projectDetail.View()
	case viewSessions:
		content = m.sessions.View()
	case viewChat:
		content = m.chat.View()
	case viewCreate:
		content = m.create.View()
	case viewEdit:
		content = m.edit.View()
	case viewCreateProject:
		content = m.createProject.View()
	case viewEditProject:
		content = m.editProject.View()
	case viewImport:
		content = m.importForm.View()
	case viewDiff:
		content = m.diff.View()
	case viewMoneypennies:
		content = m.moneypennies.View()
	case viewShell:
		content = m.shell.View()
	case viewWizard:
		content = m.wizard.View()
	}

	statusBar := m.renderStatusBar()
	return content + "\n" + statusBar
}

func (m Model) renderStatusBar() string {
	var keys []string
	switch m.currentView {
	case viewDashboard:
		completedLabel := " show done"
		if m.dashboard.showAll {
			completedLabel = " hide done"
		}
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
			statusKeyStyle.Render("a") + statusDescStyle.Render(completedLabel),
			statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
			statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
			statusKeyStyle.Render("m") + statusDescStyle.Render(" moneypennies"),
			statusKeyStyle.Render("p") + statusDescStyle.Render(" projects"),
			statusKeyStyle.Render("l") + statusDescStyle.Render(" all sessions"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("q") + statusDescStyle.Render(" quit"),
		}
	case viewProjects:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" open"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewEditProject:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" save"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("^U") + statusDescStyle.Render(" clear"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewProjectDetail:
		completedLabel := " show done"
		if m.projectDetail.showAll {
			completedLabel = " hide done"
		}
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
			statusKeyStyle.Render("a") + statusDescStyle.Render(completedLabel),
			statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
			statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewSessions:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
			statusKeyStyle.Render("i") + statusDescStyle.Render(" import"),
			statusKeyStyle.Render("s") + statusDescStyle.Render(" stop"),
			statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewChat:
		if m.chat.scheduling {
			if m.chat.scheduleAt == "" {
				keys = []string{
					statusKeyStyle.Render("↵") + statusDescStyle.Render(" confirm time"),
					statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
				}
			} else {
				keys = []string{
					statusKeyStyle.Render("↵") + statusDescStyle.Render(" schedule"),
					statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
				}
			}
		} else if m.chat.commandMode {
			keys = []string{
				statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
				statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
				statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
				statusKeyStyle.Render("s") + statusDescStyle.Render(" stop"),
				statusKeyStyle.Render("t") + statusDescStyle.Render(" schedule"),
				statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" resume"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" leave"),
				statusKeyStyle.Render("^U/^D") + statusDescStyle.Render(" scroll"),
			}
		} else {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" send"),
				statusKeyStyle.Render("^J") + statusDescStyle.Render(" newline"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" commands"),
				statusKeyStyle.Render("^U/^D") + statusDescStyle.Render(" scroll"),
			}
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
	case viewCreateProject:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" create"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewImport:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" import"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewMoneypennies:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" ping"),
			statusKeyStyle.Render("s") + statusDescStyle.Render(" set default"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewDiff:
		keys = []string{
			statusKeyStyle.Render("↑↓") + statusDescStyle.Render(" scroll"),
			statusKeyStyle.Render("pgup/dn") + statusDescStyle.Render(" page"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewShell:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" run"),
			statusKeyStyle.Render("^U") + statusDescStyle.Render(" clear"),
			statusKeyStyle.Render("pgup/dn") + statusDescStyle.Render(" scroll"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewWizard:
		switch m.wizard.step {
		case wizardStepMoneypenny:
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" select"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		case wizardStepPath:
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" open"),
				statusKeyStyle.Render("tab") + statusDescStyle.Render(" confirm path"),
				statusKeyStyle.Render("⌫") + statusDescStyle.Render(" up"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		case wizardStepForm:
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" create"),
				statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		}
	}

	left := lipgloss.JoinHorizontal(lipgloss.Left, keys...)
	right := ""
	if m.statusMsg != "" {
		right = statusDescStyle.Render(m.statusMsg)
	} else if m.version != "" {
		right = statusDescStyle.Render("v" + m.version)
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		// Not enough room for right side — drop it.
		right = ""
		gap = m.width - lipgloss.Width(left)
		if gap < 0 {
			gap = 0
		}
	}
	// Use a plain style (no padding) for the gap filler so it doesn't add extra width.
	gapStyle := lipgloss.NewStyle().Background(colorPrimary)
	padding := gapStyle.Render(fmt.Sprintf("%*s", gap, ""))

	return left + padding + right
}

// Run starts the TUI.
func Run(version string) error {
	p := tea.NewProgram(New(version), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
