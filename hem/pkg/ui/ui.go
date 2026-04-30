package ui

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"james/hem/pkg/commands"
	"james/hem/pkg/hemclient"
)

// uiLog is the TUI debug logger. Writes to ~/.config/james/hem/ui.log.
// Initialized lazily on first use; nil-safe (logs are no-ops if init fails).
var uiLog *log.Logger

func initUILog() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	logDir := filepath.Join(home, ".config", "james", "hem")
	os.MkdirAll(logDir, 0700)
	f, err := os.OpenFile(filepath.Join(logDir, "ui.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	uiLog = log.New(f, "[ui] ", log.Ltime|log.Lmicroseconds)
}

func uilog(format string, args ...interface{}) {
	if uiLog != nil {
		uiLog.Printf(format, args...)
	}
}

type view int

const (
	viewDashboard view = iota
	viewProjects
	viewProjectDetail
	viewSessions
	viewChat
	viewCreate
	viewEdit
	viewImport
	viewDiff
	viewEditProject
	viewMoneypennies
	viewAddMoneypenny
	viewShell
	viewWizard
	viewTemplatePicker
	viewCreateTemplate
	viewMemory
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
	editProject   editProjectModel
	importForm    importModel
	diff          diffModel
	memory        memoryModel
	moneypennies    moneypenniesModel
	addMoneypenny   addMoneypennyModel
	shell           shellModel
	wizard         wizardModel
	templatePicker templatePickerModel
	createTemplate createTemplateModel
	parentChats       []chatModel       // stack for subagent navigation
	chatDrafts        map[string]string // sessionID → unsent input text
	width             int
	height            int
	client            *client
	statusMsg         string
	version           string
	silent            bool
	lastSessionStates map[string]string // sessionID → last known mpStatus
}

// UIOptions configures the TUI.
type UIOptions struct {
	Silent           bool
	Sender           hemclient.Sender
	UseNotifications bool // feature flag: use broadcast notifications (--ff-use-notifications)
}

// New creates the initial UI model.
func New(version string, opts ...UIOptions) Model {
	var c *client
	var silent bool
	if len(opts) > 0 {
		if opts[0].Sender != nil {
			c = newMI6Client(opts[0].Sender)
		}
		silent = opts[0].Silent
	}
	if c == nil {
		c = newClient()
	}
	if len(opts) > 0 {
		c.useNotifications = opts[0].UseNotifications
	}
	return Model{
		currentView:       viewDashboard,
		dashboard:         newDashboardModel(c),
		sessions:          newSessionsModel(c),
		chatDrafts:        make(map[string]string),
		client:            c,
		version:           version,
		silent:            silent,
		lastSessionStates: make(map[string]string),
	}
}

// soundSettingLoadedMsg is dispatched once the sound default has been read
// from the server at startup. It overrides the CLI --silent flag only when
// the user has explicitly stored "off" via the in-TUI toggle.
type soundSettingLoadedMsg struct {
	value string
}

func (m Model) loadSoundSetting() tea.Cmd {
	return func() tea.Msg {
		v, _ := m.client.getDefault("sound")
		return soundSettingLoadedMsg{value: v}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.dashboard.Init(), m.loadSoundSetting())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := m.viewHeight()
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
		m.templatePicker.width = msg.Width
		m.templatePicker.height = h
		m.createTemplate.width = msg.Width
		m.createTemplate.height = h
		return m, nil

	case tea.KeyMsg:
		// Global keys.
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.currentView == viewDashboard && !m.dashboard.filtering {
				return m, tea.Quit
			}
		case "esc":
			// When filtering, let the view model handle esc.
			if m.currentView == viewDashboard && m.dashboard.filtering {
				var cmd tea.Cmd
				m.dashboard, cmd = m.dashboard.Update(msg)
				return m, cmd
			}
			if m.currentView == viewProjectDetail && m.projectDetail.filtering {
				var cmd tea.Cmd
				m.projectDetail, cmd = m.projectDetail.Update(msg)
				return m, cmd
			}
			if m.currentView == viewSessions && m.sessions.filtering {
				var cmd tea.Cmd
				m.sessions, cmd = m.sessions.Update(msg)
				return m, cmd
			}
			if m.currentView == viewChat {
				if m.chat.creatingSubagent {
					m.chat.creatingSubagent = false
					m.chat.subagentPrompt = ""
					m.chat.subagentPromptPos = 0
					return m, nil
				}
				if m.chat.pickingSubagent {
					m.chat.pickingSubagent = false
					return m, nil
				}
				if m.chat.pickingSchedule {
					m.chat.pickingSchedule = false
					m.chat.confirmDeleteSchedule = false
					return m, nil
				}
				if m.chat.viewingFile {
					if m.chat.viewFileMode != fileViewModeView {
						// Let the file viewer handle Esc for comment modes.
						var cmd tea.Cmd
						m.chat, cmd = m.chat.Update(msg)
						return m, cmd
					}
					m.chat.viewingFile = false
					return m, nil
				}
				if m.chat.downloadMode {
					m.chat.downloadMode = false
					m.chat.downloadErr = nil
					return m, nil
				}
				if m.chat.browsingFiles {
					m.chat.browsingFiles = false
					m.chat.browserErr = nil
					return m, nil
				}
				if m.chat.scheduling {
					m.chat.scheduling = false
					m.chat.scheduleAt = ""
					m.chat.chatInput.Reset()
					return m, nil
				}
				if !m.chat.commandMode {
					m.chat.commandMode = true
					return m, nil
				}
				// Second Esc: exit command mode back to input.
				m.chat.commandMode = false
				m.chat.confirmDelete = false
				return m, nil
			}
			if m.currentView == viewDiff && m.diff.tab == diffTabCommit {
				m.diff.tab = m.diff.prevTab
				m.diff.commitDetail = ""
				m.diff.commitHash = ""
				m.diff.commitErr2 = nil
				return m, nil
			}
			if m.currentView == viewDiff && m.diff.mode == diffModeCommitMsg {
				m.diff.mode = diffModeView
				m.diff.commitMsg = ""
				m.diff.commitErr = nil
				return m, nil
			}
			// Diff review modes: esc cancels current input mode.
			if m.currentView == viewDiff && m.diff.mode != diffModeView {
				m.diff.mode = diffModeView
				m.diff.lineInput.Reset()
				m.diff.commentInput.Reset()
				m.diff.reviewPrompt.Reset()
				return m, nil
			}
			// Clear dashboard/projectDetail filter on esc.
			if m.currentView == viewDashboard && m.dashboard.filterText != "" {
				m.dashboard.filterText = ""
				m.dashboard.filtering = false
				m.dashboard.cursor = 0
				return m, nil
			}
			if m.currentView == viewProjectDetail && m.projectDetail.filterText != "" {
				m.projectDetail.filterText = ""
				m.projectDetail.filtering = false
				m.projectDetail.cursor = 0
				return m, nil
			}
			// Clear sessions filter on esc.
			if m.currentView == viewSessions && m.sessions.filterText != "" {
				m.sessions.filterText = ""
				m.sessions.filtering = false
				m.sessions.cursor = 0
				return m, nil
			}
			// Confirm quit from diff if there are pending review comments.
			if m.currentView == viewDiff && m.diff.shouldConfirmQuit() {
				return m, nil
			}
			return m.handleEsc()
		}

		// q exits the diff view (like Esc) when not in an input mode.
		if msg.String() == "q" && m.currentView == viewDiff && m.diff.mode == diffModeView {
			if m.diff.shouldConfirmQuit() {
				return m, nil
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
		case viewImport:
			return m.updateImport(msg)
		case viewDiff:
			return m.updateDiff(msg)
		case viewEditProject:
			return m.updateEditProject(msg)
		case viewMoneypennies:
			return m.updateMoneypennies(msg)
		case viewAddMoneypenny:
			return m.updateAddMoneypenny(msg)
		case viewShell:
			return m.updateShell(msg)
		case viewWizard:
			return m.updateWizard(msg)
		case viewTemplatePicker:
			return m.updateTemplatePicker(msg)
		case viewCreateTemplate:
			return m.updateCreateTemplate(msg)
		case viewMemory:
			return m.updateMemory(msg)
		}

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			switch m.currentView {
			case viewChat:
				m.chat.scroll += 3
				if m.chat.scroll > 0 && !m.chat.loadingMore && len(m.chat.conversation) < m.chat.totalTurns {
					m.chat.loadingMore = true
					return m, m.chat.loadOlderHistory()
				}
			case viewDiff:
				m.diff.scroll -= 3
				if m.diff.scroll < 0 {
					m.diff.scroll = 0
				}
			case viewShell:
				m.shell.scroll += 3
			}
		case tea.MouseButtonWheelDown:
			switch m.currentView {
			case viewChat:
				m.chat.scroll -= 3
				if m.chat.scroll < 0 {
					m.chat.scroll = 0
				}
			case viewDiff:
				m.diff.scroll += 3
			case viewShell:
				m.shell.scroll -= 3
				if m.shell.scroll < 0 {
					m.shell.scroll = 0
				}
			}
		}
		// Consume all mouse events (don't pass through) to prevent escape sequence leaks.
		return m, nil

	// Route messages to appropriate view.
	case sessionCompletedMsg, dashboardDeletedMsg:
		if m.currentView == viewProjectDetail {
			var cmd tea.Cmd
			m.projectDetail, cmd = m.projectDetail.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd

	case moneypenniesLoadedMsg, moneypennyDeletedMsg, moneypennyPingedMsg, moneypennyDefaultSetMsg, moneypennyEnabledMsg:
		var cmd tea.Cmd
		m.moneypennies, cmd = m.moneypennies.Update(msg)
		return m, cmd

	case moneypennyAddedMsg:
		if msg.err != nil {
			m.addMoneypenny.err = msg.err
			m.addMoneypenny.creating = false
			return m, nil
		}
		m.statusMsg = "Moneypenny added"
		m.currentView = viewMoneypennies
		m.moneypennies = newMoneypenniesModel(m.client)
		m.moneypennies.width = m.width
		m.moneypennies.height = m.viewHeight()
		return m, m.moneypennies.loadMoneypennies()

	case wizardMPLoadedMsg, wizardDirLoadedMsg, wizardProjectLoadedMsg, wizardProjectsLoadedMsg, wizardModelsLoadedMsg:
		var cmd tea.Cmd
		m.wizard, cmd = m.wizard.Update(msg)
		return m, cmd

	case templatesLoadedMsg:
		var cmd tea.Cmd
		m.templatePicker, cmd = m.templatePicker.Update(msg)
		return m, cmd

	case templateUsedMsg:
		if msg.err != nil {
			m.statusMsg = fmt.Sprintf("Error: %v", msg.err)
			return m, nil
		}
		sid := msg.sessionID
		if len(sid) > 12 {
			sid = sid[:12]
		}
		m.statusMsg = fmt.Sprintf("Session created: %s", sid)
		if m.previousView == viewDashboard {
			m.currentView = viewDashboard
			m.dashboard.loading = true
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
		}
		m.currentView = viewProjectDetail
		m.projectDetail.loading = true
		return m, m.projectDetail.loadDashboard()

	case templateDeletedMsg:
		var cmd tea.Cmd
		m.templatePicker, cmd = m.templatePicker.Update(msg)
		return m, cmd

	case templateProjectLoadedMsg:
		var cmd tea.Cmd
		m.createTemplate, cmd = m.createTemplate.Update(msg)
		return m, cmd

	case templateCreatedMsg:
		if msg.err != nil {
			m.createTemplate.creating = false
			m.createTemplate.err = msg.err
			return m, nil
		}
		m.statusMsg = "Template created"
		m.templatePicker.loading = true
		m.currentView = viewTemplatePicker
		return m, m.templatePicker.loadTemplates()

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
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
		}

	case chatSubagentDeletedMsg:
		if msg.err != nil {
			m.chat.err = msg.err
			return m, nil
		}
		// Remove the deleted subagent from the list.
		for i, sub := range m.chat.subagents {
			if sub.SessionID == msg.sessionID {
				m.chat.subagents = append(m.chat.subagents[:i], m.chat.subagents[i+1:]...)
				break
			}
		}
		// Adjust cursor if it's now out of bounds.
		totalItems := len(m.chat.subagents) + 1
		if m.chat.subagentCursor >= totalItems {
			m.chat.subagentCursor = totalItems - 1
		}
		m.statusMsg = "Subagent deleted"
		return m, nil

	case dashboardPollTickMsg:
		// Always reload the main dashboard in background regardless of current view,
		// so session states stay fresh and notifications can be detected.
		if !m.dashboard.loading {
			cmds := []tea.Cmd{m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive()}
			// Also refresh project detail if active.
			if m.currentView == viewProjectDetail && !m.projectDetail.loading {
				cmds = append(cmds, m.projectDetail.loadDashboard())
			}
			return m, tea.Batch(cmds...)
		}
		return m, m.dashboard.dashboardPollTickAdaptive()

	case dashboardLoadedMsg:
		// Detect working→ready transitions for notifications.
		if msg.projectFilter == "" && msg.err == nil && !m.silent {
			for _, entry := range msg.entries {
				prev := m.lastSessionStates[entry.SessionID]
				if prev == "working" && entry.MPStatus == "ready" {
					go commands.PlayNotificationSound()
					name := entry.Name
					if name == "" {
						name = entry.SessionID[:12]
					}
					m.statusMsg = fmt.Sprintf("Session ready: %s", name)
					break
				}
			}
			for _, entry := range msg.entries {
				m.lastSessionStates[entry.SessionID] = entry.MPStatus
			}
		}
		// Route to the matching dashboard instance based on projectFilter.
		if msg.projectFilter == "" || msg.projectFilter == m.dashboard.projectFilter {
			var cmd tea.Cmd
			m.dashboard, cmd = m.dashboard.Update(msg)
			return m, cmd
		}
		if msg.projectFilter == m.projectDetail.projectFilter {
			var cmd tea.Cmd
			m.projectDetail, cmd = m.projectDetail.Update(msg)
			return m, cmd
		}
		return m, nil

	case soundSettingLoadedMsg:
		// Honor stored "off" setting; "on" or empty leaves the CLI flag value.
		if msg.value == "off" {
			m.silent = true
		} else if msg.value == "on" {
			m.silent = false
		}
		return m, nil

	case chatPollTickMsg:
		// Only process poll ticks when chat is active.
		if m.currentView == viewChat {
			var cmd tea.Cmd
			m.chat, cmd = m.chat.Update(msg)
			return m, cmd
		}
		// Discard tick if not in chat view.
		return m, nil

	case chatOpenSubagentMsg:
		// Push current chat onto parent stack, open subagent chat.
		m.parentChats = append(m.parentChats, m.chat)
		m.chat = newChatModel(m.client, msg.sessionID, msg.name, m.chat.moneypennyName)
		m.chat.width = m.width
		m.chat.height = m.viewHeight()
		m.chat.isSubagent = true
		return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())

	case broadcastMsg, broadcastReconnectMsg:
		// Route dashboard broadcast messages.
		if m.currentView == viewProjectDetail {
			var cmd tea.Cmd
			m.projectDetail, cmd = m.projectDetail.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd

	case chatBroadcastMsg, chatBroadcastReconnectMsg:
		// Route chat broadcast messages.
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd

	case historyLoadedMsg, messageSentMsg, olderHistoryLoadedMsg,
		activityLoadedMsg, schedulesLoadedMsg, subagentsLoadedMsg, scheduleCreatedMsg, scheduleCancelledMsg,
		chatSubagentCreatedMsg, browserLoadedMsg, fileTransferredMsg,
		fileContentLoadedMsg, fileDownloadedMsg, downloadBrowserLoadedMsg:
		uilog("routing chat msg type=%T to chat.Update (currentView=%d)", msg, m.currentView)
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd

	case fileReviewSubmitMsg:
		// Close file viewer and send the review prompt in chat.
		m.chat.viewingFile = false
		m.chat.conversation = append(m.chat.conversation, conversationTurn{
			Role:    "user",
			Content: msg.prompt,
		})
		m.chat.recentCount++
		m.chat.totalTurns++
		m.chat.scroll = 0
		return m, m.chat.sendMessage(msg.prompt)

	case diffCommitDoneMsg:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
		if msg.err == nil {
			if msg.pushed {
				m.statusMsg = "Committed and pushed"
			} else {
				m.statusMsg = "Committed"
			}
		}
		return m, cmd

	case diffPushDoneMsg:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
		if msg.err == nil {
			m.statusMsg = "Pushed"
		}
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
		m.sessions.height = m.viewHeight()
		return m, m.sessions.loadSessions()

	case shellCommandDoneMsg:
		var cmd tea.Cmd
		m.shell, cmd = m.shell.Update(msg)
		return m, cmd

	case diffLoadedMsg, gitLogLoadedMsg, gitInfoLoadedMsg, gitShowLoadedMsg:
		var cmd tea.Cmd
		m.diff, cmd = m.diff.Update(msg)
		return m, cmd

	case diffReviewSubmitMsg:
		// Switch to chat view and send the review prompt.
		m.currentView = viewChat
		m.chat.chatInput.SetValue(msg.prompt)
		// Append to conversation optimistically and send.
		m.chat.conversation = append(m.chat.conversation, conversationTurn{
			Role:    "user",
			Content: msg.prompt,
		})
		m.chat.recentCount++
		m.chat.totalTurns++
		m.chat.scroll = 0
		m.chat.chatInput.Reset()
		return m, m.chat.sendMessage(msg.prompt)

	case sessionDetailLoadedMsg, editProjectsLoadedMsg, editModelsLoadedMsg:
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
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
		}
		m.currentView = viewSessions
		m.sessions.loading = true
		return m, m.sessions.loadSessions()

	case memoryLoadedMsg, memorySavedMsg:
		var cmd tea.Cmd
		m.memory, cmd = m.memory.Update(msg)
		return m, cmd

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
		m.projects.height = m.viewHeight()
		return m, m.projects.loadProjects()

	case projectCreatedMsg:
		pm := msg
		if pm.err != nil {
			m.wizard.err = pm.err
			m.wizard.creating = false
			return m, nil
		}
		m.statusMsg = "Project created"
		m.currentView = viewProjects
		m.projects = newProjectsModel(m.client)
		m.projects.width = m.width
		m.projects.height = m.viewHeight()
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
				return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
			}
		}

		var mpName string
		if isWizard {
			mpName = m.wizard.selectedMP
		}
		m.chat = newChatModel(m.client, cm.sessionID, "", mpName)
		m.chat.width = m.width
		m.chat.height = m.viewHeight()
		if cm.response != "" {
			var prompt string
			if isWizard && len(m.wizard.fields) > 0 {
				prompt = m.wizard.fields[0].value
			} else if !isWizard && len(m.create.fields) > 0 {
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
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
		}
		return m, m.chat.chatPollTickAdaptive()
	}

	return m, nil
}

func (m Model) withChatDraftSaved() Model {
	if m.chat.sessionID != "" {
		if !m.chat.chatInput.IsEmpty() {
			m.chatDrafts[m.chat.sessionID] = m.chat.chatInput.Value()
		} else {
			delete(m.chatDrafts, m.chat.sessionID)
		}
	}
	return m
}

func (m Model) withChatDraftRestored() Model {
	if draft, ok := m.chatDrafts[m.chat.sessionID]; ok {
		m.chat.chatInput.SetValue(draft)
	}
	return m
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
		return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
	case viewProjectDetail:
		m.currentView = viewProjects
		m.statusMsg = ""
		m.projects.loading = true
		return m, m.projects.loadProjects()
	case viewSessions:
		m.currentView = viewDashboard
		m.statusMsg = ""
		m.dashboard.loading = true
		return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
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
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
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
	case viewEditProject:
		m.currentView = viewProjects
		m.statusMsg = ""
		m.projects.loading = true
		return m, m.projects.loadProjects()
	case viewAddMoneypenny:
		m.currentView = viewMoneypennies
		m.statusMsg = ""
		m.moneypennies.loading = true
		return m, m.moneypennies.loadMoneypennies()
	case viewMoneypennies:
		m.currentView = viewDashboard
		m.statusMsg = ""
		m.dashboard.loading = true
		return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
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
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
		}
	case viewTemplatePicker:
		m.statusMsg = ""
		if m.previousView == viewDashboard {
			m.currentView = viewDashboard
			m.dashboard.loading = true
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
		}
		m.currentView = viewProjectDetail
		m.projectDetail.loading = true
		return m, m.projectDetail.loadDashboard()
	case viewCreateTemplate:
		m.currentView = viewTemplatePicker
		m.statusMsg = ""
		m.templatePicker.loading = true
		return m, m.templatePicker.loadTemplates()
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
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
		}
	case viewMemory:
		m.currentView = viewChat
		m.statusMsg = ""
		return m, nil
	}
	return m, nil
}

func (m Model) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When filtering, route all keys to dashboard model for input handling.
	if m.dashboard.filtering {
		var cmd tea.Cmd
		m.dashboard, cmd = m.dashboard.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "enter":
		e := m.dashboard.selectedEntry()
		uilog("dashboard: enter pressed, selectedEntry=%v cursor=%d entries=%d filtered=%d", e != nil, m.dashboard.cursor, len(m.dashboard.entries), len(m.dashboard.filteredEntries()))
		if e != nil {
			uilog("dashboard: opening session id=%s name=%q mp=%s status=%s parent=%s", e.SessionID, e.Name, e.Moneypenny, e.MPStatus, e.ParentSessionID)
			if e.ParentSessionID != "" {
				// Opening a subagent: push parent chat onto stack, then open sub.
				parentName := ""
				for _, pe := range m.dashboard.entries {
					if pe.SessionID == e.ParentSessionID {
						parentName = pe.Name
						break
					}
				}
				parentChat := newChatModel(m.client, e.ParentSessionID, parentName, e.Moneypenny)
				parentChat.width = m.width
				parentChat.height = m.viewHeight()
				m.parentChats = append(m.parentChats, parentChat)
				m.chat = newChatModel(m.client, e.SessionID, e.Name, e.Moneypenny)
				m.chat.isSubagent = true
			} else {
				m.chat = newChatModel(m.client, e.SessionID, e.Name, e.Moneypenny)
				m = m.withChatDraftRestored()
			}
			m.chat.width = m.width
			m.chat.height = m.viewHeight()
			m.currentView = viewChat
			m.previousView = viewDashboard
			uilog("dashboard: switched to viewChat, loading history+activity")
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
		}
		uilog("dashboard: enter pressed but no entry selected")
	case "c":
		e := m.dashboard.selectedEntry()
		if e != nil {
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
		m.wizard.height = m.viewHeight()
		m.currentView = viewWizard
		m.previousView = viewDashboard
		return m, m.wizard.Init()
	case "e":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.edit = newEditModel(m.client, e.SessionID)
			m.edit.width = m.width
			m.edit.height = m.viewHeight()
			m.currentView = viewEdit
			return m, tea.Batch(m.edit.loadDetail(), m.edit.loadProjects())
		}
	case "g":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.diff = newDiffModel(m.client, e.SessionID)
			m.diff.width = m.width
			m.diff.height = m.viewHeight()
			m.currentView = viewDiff
			m.previousView = viewDashboard
			return m, tea.Batch(m.diff.loadDiff(), m.diff.loadGitLog(), m.diff.loadGitInfo())
		}
	case "x":
		e := m.dashboard.selectedEntry()
		if e != nil {
			m.shell = newShellModelFromSession(m.client, e.SessionID, e.Name)
			m.shell.width = m.width
			m.shell.height = m.viewHeight()
			m.currentView = viewShell
			m.previousView = viewDashboard
			return m, nil
		}
	case "l":
		m.sessions = newSessionsModel(m.client)
		m.sessions.width = m.width
		m.sessions.height = m.viewHeight()
		m.currentView = viewSessions
		return m, m.sessions.loadSessions()
	case "m":
		m.moneypennies = newMoneypenniesModel(m.client)
		m.moneypennies.width = m.width
		m.moneypennies.height = m.viewHeight()
		m.currentView = viewMoneypennies
		return m, m.moneypennies.loadMoneypennies()
	case "p":
		m.projects = newProjectsModel(m.client)
		m.projects.width = m.width
		m.projects.height = m.viewHeight()
		m.currentView = viewProjects
		return m, m.projects.loadProjects()
	case "s":
		m.dashboard.showSubs = !m.dashboard.showSubs
		m.dashboard.loading = true
		if m.dashboard.showSubs {
			m.statusMsg = "Showing all subagents"
		} else {
			m.statusMsg = "Showing only active subagents"
		}
		return m, m.dashboard.loadDashboard()
	case "a":
		m.dashboard.showAll = !m.dashboard.showAll
		m.dashboard.loading = true
		return m, m.dashboard.loadDashboard()
	case "b":
		m.silent = !m.silent
		value := "on"
		if m.silent {
			value = "off"
			m.statusMsg = "Sound notifications: 🔕 off"
		} else {
			m.statusMsg = "Sound notifications: 🔔 on"
		}
		go func(v string) { _ = m.client.setDefault("sound", v) }(value)
		return m, nil
	case "t":
		m.templatePicker = newTemplatePickerModel(m.client, "")
		m.templatePicker.width = m.width
		m.templatePicker.height = m.viewHeight()
		m.currentView = viewTemplatePicker
		m.previousView = viewDashboard
		return m, m.templatePicker.loadTemplates()
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
			m.projectDetail.height = m.viewHeight()
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
			m.editProject.height = m.viewHeight()
			m.currentView = viewEditProject
			return m, nil
		}
	case "n":
		m.wizard = newProjectWizardModel(m.client)
		m.wizard.width = m.width
		m.wizard.height = m.viewHeight()
		m.currentView = viewWizard
		m.previousView = viewProjects
		return m, m.wizard.Init()
	default:
		var cmd tea.Cmd
		m.projects, cmd = m.projects.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateProjectDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When filtering, route all keys to projectDetail model for input handling.
	if m.projectDetail.filtering {
		var cmd tea.Cmd
		m.projectDetail, cmd = m.projectDetail.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "enter":
		e := m.projectDetail.selectedEntry()
		uilog("projectDetail: enter pressed, selectedEntry=%v", e != nil)
		if e != nil {
			uilog("projectDetail: opening session id=%s name=%q mp=%s", e.SessionID, e.Name, e.Moneypenny)
			if e.ParentSessionID != "" {
				parentName := ""
				for _, pe := range m.projectDetail.entries {
					if pe.SessionID == e.ParentSessionID {
						parentName = pe.Name
						break
					}
				}
				parentChat := newChatModel(m.client, e.ParentSessionID, parentName, e.Moneypenny)
				parentChat.width = m.width
				parentChat.height = m.viewHeight()
				m.parentChats = append(m.parentChats, parentChat)
				m.chat = newChatModel(m.client, e.SessionID, e.Name, e.Moneypenny)
				m.chat.isSubagent = true
			} else {
				m.chat = newChatModel(m.client, e.SessionID, e.Name, e.Moneypenny)
				m = m.withChatDraftRestored()
			}
			m.chat.width = m.width
			m.chat.height = m.viewHeight()
			m.currentView = viewChat
			m.previousView = viewProjectDetail
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
		}
	case "c":
		e := m.projectDetail.selectedEntry()
		if e != nil {
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
			m.edit.height = m.viewHeight()
			m.currentView = viewEdit
			return m, tea.Batch(m.edit.loadDetail(), m.edit.loadProjects())
		}
	case "g":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.diff = newDiffModel(m.client, e.SessionID)
			m.diff.width = m.width
			m.diff.height = m.viewHeight()
			m.currentView = viewDiff
			m.previousView = viewProjectDetail
			return m, tea.Batch(m.diff.loadDiff(), m.diff.loadGitLog(), m.diff.loadGitInfo())
		}
	case "n":
		m.wizard = newWizardModelForProject(m.client, m.projectDetail.projectFilter)
		m.wizard.width = m.width
		m.wizard.height = m.viewHeight()
		m.currentView = viewWizard
		m.previousView = viewProjectDetail
		return m, m.wizard.Init()
	case "t":
		m.templatePicker = newTemplatePickerModel(m.client, m.projectDetail.projectFilter)
		m.templatePicker.width = m.width
		m.templatePicker.height = m.viewHeight()
		m.currentView = viewTemplatePicker
		m.previousView = viewProjectDetail
		return m, m.templatePicker.loadTemplates()
	case "s":
		m.projectDetail.showSubs = !m.projectDetail.showSubs
		m.projectDetail.loading = true
		if m.projectDetail.showSubs {
			m.statusMsg = "Showing all subagents"
		} else {
			m.statusMsg = "Showing only active subagents"
		}
		return m, m.projectDetail.loadDashboard()
	case "a":
		m.projectDetail.showAll = !m.projectDetail.showAll
		m.projectDetail.loading = true
		return m, m.projectDetail.loadDashboard()
	case "x":
		e := m.projectDetail.selectedEntry()
		if e != nil {
			m.shell = newShellModelFromSession(m.client, e.SessionID, e.Name)
			m.shell.width = m.width
			m.shell.height = m.viewHeight()
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

func (m Model) updateTemplatePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		t := m.templatePicker.selectedTemplate()
		if t != nil {
			return m, m.templatePicker.useTemplate(t.ID)
		}
	case "n":
		if m.templatePicker.projectName != "" {
			m.createTemplate = newCreateTemplateModel(m.client, m.templatePicker.projectName)
			m.createTemplate.width = m.width
			m.createTemplate.height = m.viewHeight()
			m.currentView = viewCreateTemplate
			return m, m.createTemplate.loadProjectPath()
		}
	case "d":
		if m.templatePicker.projectName != "" {
			t := m.templatePicker.selectedTemplate()
			if t != nil {
				return m, m.templatePicker.deleteTemplate(t.ID)
			}
		}
	default:
		var cmd tea.Cmd
		m.templatePicker, cmd = m.templatePicker.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) updateCreateTemplate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.createTemplate, cmd = m.createTemplate.Update(msg)
	return m, cmd
}

func (m Model) updateSessions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When filtering, route all keys to sessions model for input handling.
	if m.sessions.filtering {
		var cmd tea.Cmd
		m.sessions, cmd = m.sessions.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "enter":
		s := m.sessions.selectedSession()
		uilog("sessions: enter pressed, selectedSession=%v cursor=%d sessions=%d", s != nil, m.sessions.cursor, len(m.sessions.sessions))
		if s != nil {
			uilog("sessions: opening session id=%s name=%q mp=%s", s.SessionID, s.Name, s.Moneypenny)
			m.chat = newChatModel(m.client, s.SessionID, s.Name, s.Moneypenny)
			m.chat.width = m.width
			m.chat.height = m.viewHeight()
			m = m.withChatDraftRestored()
			m.currentView = viewChat
			m.previousView = viewSessions
			uilog("sessions: switched to viewChat, loading history+activity")
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
		}
	case "n":
		m.wizard = newWizardModel(m.client)
		m.wizard.width = m.width
		m.wizard.height = m.viewHeight()
		m.currentView = viewWizard
		m.previousView = viewSessions
		return m, m.wizard.Init()
	case "e":
		s := m.sessions.selectedSession()
		if s != nil {
			m.edit = newEditModel(m.client, s.SessionID)
			m.edit.width = m.width
			m.edit.height = m.viewHeight()
			m.currentView = viewEdit
			return m, tea.Batch(m.edit.loadDetail(), m.edit.loadProjects())
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
		m.importForm.height = m.viewHeight()
		m.currentView = viewImport
		return m, nil
	case "g":
		s := m.sessions.selectedSession()
		if s != nil {
			m.diff = newDiffModel(m.client, s.SessionID)
			m.diff.width = m.width
			m.diff.height = m.viewHeight()
			m.currentView = viewDiff
			m.previousView = viewSessions
			return m, tea.Batch(m.diff.loadDiff(), m.diff.loadGitLog(), m.diff.loadGitInfo())
		}
	case "x":
		s := m.sessions.selectedSession()
		if s != nil {
			m.shell = newShellModelFromSession(m.client, s.SessionID, s.Name)
			m.shell.width = m.width
			m.shell.height = m.viewHeight()
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
	if m.chat.commandMode && !m.chat.pickingSubagent && !m.chat.creatingSubagent && !m.chat.pickingSchedule {
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
			m.edit.height = m.viewHeight()
			m.currentView = viewEdit
			m.previousView = viewChat
			return m, tea.Batch(m.edit.loadDetail(), m.edit.loadProjects())
		case "g":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.diff = newDiffModel(m.client, m.chat.sessionID)
			m.diff.width = m.width
			m.diff.height = m.viewHeight()
			m.currentView = viewDiff
			m.previousView = viewChat
			return m, tea.Batch(m.diff.loadDiff(), m.diff.loadGitLog(), m.diff.loadGitInfo())
		case "x":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.shell = newShellModelFromSession(m.client, m.chat.sessionID, m.chat.sessionName)
			m.shell.width = m.width
			m.shell.height = m.viewHeight()
			m.currentView = viewShell
			m.previousView = viewChat
			return m, nil
		case "t":
			m.chat.confirmDelete = false
			if len(m.chat.pendingSchedules()) > 0 {
				// Show schedule picker to manage existing schedules.
				m.chat.pickingSchedule = true
				m.chat.scheduleCursor = 0
				m.chat.confirmDeleteSchedule = false
				return m, nil
			}
			// No pending schedules — go straight to create.
			m.chat.commandMode = false
			m.chat.scheduling = true
			m.chat.scheduleAt = ""
			m.chat.chatInput.Reset()
			return m, nil
		case "a":
			m.chat.confirmDelete = false
			m.chat.pickingSubagent = true
			m.chat.subagentCursor = 0
			return m, nil
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.chat.confirmDelete = false
			idx := int(msg.String()[0]-'0') - 1
			if idx < len(m.chat.subagents) {
				sub := m.chat.subagents[idx]
				return m, func() tea.Msg {
					return chatOpenSubagentMsg{sessionID: sub.SessionID, name: sub.Name}
				}
			}
			return m, nil
		case "r":
			m.chat.confirmDelete = false
			return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity())
		case "m":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m.memory = newMemoryModel(m.client, m.chat.sessionID)
			m.memory.width = m.width
			m.memory.height = m.viewHeight()
			m.currentView = viewMemory
			m.previousView = viewChat
			return m, m.memory.loadMemory()
		case "f":
			m.chat.confirmDelete = false
			m.chat.browsingFiles = true
			m.chat.browserLoading = true
			m.chat.browserErr = nil
			// Start browsing from session's working directory (fetch from moneypenny).
			startPath := ""
			return m, func() tea.Msg {
				// Get session path from moneypenny.
				resp, err := m.client.send("show", "session", m.chat.sessionID)
				if err == nil && resp.Status == "ok" {
					var detail struct {
						Path string `json:"path"`
					}
					json.Unmarshal(resp.Data, &detail)
					startPath = detail.Path
				}
				if startPath == "" {
					startPath = "/"
				}
				entries, err := m.client.listDirectory(m.chat.moneypennyName, startPath, m.chat.browserShowHidden)
				if err != nil {
					return browserLoadedMsg{path: startPath, err: err}
				}
				return browserLoadedMsg{path: startPath, entries: entries}
			}
		case "q":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			// If viewing a subagent, pop back to parent chat.
			if len(m.parentChats) > 0 {
				m.chat = m.parentChats[len(m.parentChats)-1]
				m.parentChats = m.parentChats[:len(m.parentChats)-1]
				return m, tea.Batch(m.chat.loadHistory(), m.chat.loadActivity(), m.chat.chatPollTickAdaptive())
			}
			// Save draft and leave to previous view.
			m = m.withChatDraftSaved()
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
				return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
			}
		case "Q":
			m.chat.confirmDelete = false
			m.chat.commandMode = false
			m = m.withChatDraftSaved()
			m.parentChats = nil // discard subagent stack
			m.currentView = viewDashboard
			m.dashboard.loading = true
			m.statusMsg = ""
			return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
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

func (m Model) updateMemory(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.memory, cmd = m.memory.Update(msg)
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
	case "n":
		m.addMoneypenny = newAddMoneypennyModel(m.client)
		m.addMoneypenny.width = m.width
		m.addMoneypenny.height = m.viewHeight()
		m.currentView = viewAddMoneypenny
		return m, nil
	case "e":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			return m, m.moneypennies.toggleEnabled(mp)
		}
	case "x":
		mp := m.moneypennies.selectedMoneypenny()
		if mp != nil {
			m.shell = newShellModel(m.client, mp.Name, "", "Shell: "+mp.Name)
			m.shell.width = m.width
			m.shell.height = m.viewHeight()
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

func (m Model) updateAddMoneypenny(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.addMoneypenny, cmd = m.addMoneypenny.Update(msg)
	return m, cmd
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
				return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
			case viewProjectDetail:
				m.projectDetail.loading = true
				return m, m.projectDetail.loadDashboard()
			case viewSessions:
				m.sessions.loading = true
				return m, m.sessions.loadSessions()
			default:
				m.currentView = viewDashboard
				m.dashboard.loading = true
				return m, tea.Batch(m.dashboard.loadDashboard(), m.dashboard.dashboardPollTickAdaptive())
			}
		case wizardStepPath:
			m.wizard.step = wizardStepMoneypenny
			m.wizard.err = nil
			return m, nil
		case wizardStepForm:
			m.wizard.step = wizardStepMoneypenny
			m.wizard.mpLoading = true
			m.wizard.err = nil
			return m, m.wizard.loadMoneypennies()
		}
	}
	var cmd tea.Cmd
	m.wizard, cmd = m.wizard.Update(msg)
	return m, cmd
}

// viewHeight returns the height available for view content (terminal height minus status bar).
func (m Model) viewHeight() int {
	sbLines := strings.Count(m.renderStatusBar(), "\n") + 1
	h := m.height - sbLines - 1 // -1 for \n separator
	if h < 1 {
		h = 1
	}
	return h
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
	case viewEditProject:
		content = m.editProject.View()
	case viewImport:
		content = m.importForm.View()
	case viewDiff:
		content = m.diff.View()
	case viewMoneypennies:
		content = m.moneypennies.View()
	case viewAddMoneypenny:
		content = m.addMoneypenny.View()
	case viewShell:
		content = m.shell.View()
	case viewWizard:
		content = m.wizard.View()
	case viewTemplatePicker:
		content = m.templatePicker.View()
	case viewCreateTemplate:
		content = m.createTemplate.View()
	case viewMemory:
		content = m.memory.View()
	}

	statusBar := m.renderStatusBar()
	statusBarLines := strings.Count(statusBar, "\n") + 1

	// Total output must be exactly m.height lines.
	// Layout: content lines + 1 (\n separator) + statusBarLines = m.height
	targetContentLines := m.height - statusBarLines - 1
	if targetContentLines < 1 {
		targetContentLines = 1
	}

	// Count actual content lines (number of \n = number of lines, since last line has no trailing \n).
	contentLines := strings.Count(content, "\n")

	if contentLines < targetContentLines {
		// Pad content to fill the space.
		padding := strings.Repeat("\n", targetContentLines-contentLines)
		content += padding
	}
	// Note: we don't trim — if content is too long, bubbletea will scroll,
	// which is better than cutting off the input prompt.

	return content + "\n" + statusBar
}

func (m Model) renderStatusBar() string {
	var keys []string
	switch m.currentView {
	case viewDashboard:
		if m.dashboard.filtering {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" apply"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else {
			completedLabel := " show done"
			if m.dashboard.showAll {
				completedLabel = " hide done"
			}
			subsLabel := " show subs"
			if m.dashboard.showSubs {
				subsLabel = " hide subs"
			}
			soundLabel := " 🔔 sound"
			if m.silent {
				soundLabel = " 🔕 sound"
			}
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
				statusKeyStyle.Render("/") + statusDescStyle.Render(" filter"),
				statusKeyStyle.Render("a") + statusDescStyle.Render(completedLabel),
				statusKeyStyle.Render("s") + statusDescStyle.Render(subsLabel),
				statusKeyStyle.Render("b") + statusDescStyle.Render(soundLabel),
				statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
				statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
				statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
				statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
				statusKeyStyle.Render("t") + statusDescStyle.Render(" templates"),
				statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
				statusKeyStyle.Render("m") + statusDescStyle.Render(" moneypennies"),
				statusKeyStyle.Render("p") + statusDescStyle.Render(" projects"),
				statusKeyStyle.Render("l") + statusDescStyle.Render(" all sessions"),
				statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
				statusKeyStyle.Render("q") + statusDescStyle.Render(" quit"),
			}
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
		if m.projectDetail.filtering {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" apply"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else {
			completedLabel := " show done"
			if m.projectDetail.showAll {
				completedLabel = " hide done"
			}
			subsLabel := " show subs"
			if m.projectDetail.showSubs {
				subsLabel = " hide subs"
			}
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
				statusKeyStyle.Render("/") + statusDescStyle.Render(" filter"),
				statusKeyStyle.Render("a") + statusDescStyle.Render(completedLabel),
				statusKeyStyle.Render("s") + statusDescStyle.Render(subsLabel),
				statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
				statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
				statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
				statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
				statusKeyStyle.Render("t") + statusDescStyle.Render(" template"),
				statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
				statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		}
	case viewSessions:
		if m.sessions.filtering {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" apply"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" chat"),
				statusKeyStyle.Render("/") + statusDescStyle.Render(" filter"),
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
		} else if m.chat.creatingSubagent {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" create"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else if m.chat.pickingSubagent {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" open/create"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else if m.chat.pickingSchedule {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" new"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" cancel schedule"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		} else if m.chat.commandMode {
			keys = []string{
				statusKeyStyle.Render("a") + statusDescStyle.Render(" subagents"),
				statusKeyStyle.Render("c") + statusDescStyle.Render(" complete"),
				statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
				statusKeyStyle.Render("e") + statusDescStyle.Render(" edit"),
				statusKeyStyle.Render("g") + statusDescStyle.Render(" git diff"),
				statusKeyStyle.Render("s") + statusDescStyle.Render(" stop"),
				statusKeyStyle.Render("t") + statusDescStyle.Render(" schedule"),
				statusKeyStyle.Render("f") + statusDescStyle.Render(" files"),
				statusKeyStyle.Render("m") + statusDescStyle.Render(" memory"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
				statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
				statusKeyStyle.Render("1-9") + statusDescStyle.Render(" sub#"),
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" resume"),
				statusKeyStyle.Render("q") + func() string {
					if len(m.parentChats) > 0 {
						return statusDescStyle.Render(" parent")
					}
					return statusDescStyle.Render(" leave")
				}(),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" input"),
				statusKeyStyle.Render("^U/^D") + statusDescStyle.Render(" scroll"),
			}
		} else {
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" send"),
				statusKeyStyle.Render("^J") + statusDescStyle.Render(" newline"),
				statusKeyStyle.Render("^R") + statusDescStyle.Render(" clear"),
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
	case viewImport:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" import"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewMoneypennies:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" ping"),
			statusKeyStyle.Render("n") + statusDescStyle.Render(" new"),
			statusKeyStyle.Render("s") + statusDescStyle.Render(" set default"),
			statusKeyStyle.Render("e") + statusDescStyle.Render(" enable/disable"),
			statusKeyStyle.Render("d") + statusDescStyle.Render(" delete"),
			statusKeyStyle.Render("x") + statusDescStyle.Render(" shell"),
			statusKeyStyle.Render("r") + statusDescStyle.Render(" refresh"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewAddMoneypenny:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" add"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	case viewDiff:
		if m.diff.mode == diffModeCommitMsg {
			action := "commit"
			if m.diff.amendMode && m.diff.pushAfter {
				action = "amend+force-push"
			} else if m.diff.amendMode {
				action = "amend"
			} else if m.diff.pushAfter {
				action = "commit+push"
			}
			keys = []string{
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" "+action),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
			}
		} else if m.diff.tab == diffTabCommit {
			keys = []string{
				statusKeyStyle.Render("↑↓") + statusDescStyle.Render(" scroll"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		} else if m.diff.tab == diffTabDiff {
			keys = []string{
				statusKeyStyle.Render("tab") + statusDescStyle.Render(" log"),
				statusKeyStyle.Render("↑↓") + statusDescStyle.Render(" scroll"),
				statusKeyStyle.Render("c") + statusDescStyle.Render(" commit"),
				statusKeyStyle.Render("C") + statusDescStyle.Render(" commit+push"),
				statusKeyStyle.Render("a") + statusDescStyle.Render(" amend"),
				statusKeyStyle.Render("A") + statusDescStyle.Render(" amend+push"),
				statusKeyStyle.Render("p") + statusDescStyle.Render(" push"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		} else {
			keys = []string{
				statusKeyStyle.Render("tab") + statusDescStyle.Render(" diff"),
				statusKeyStyle.Render("↑↓") + statusDescStyle.Render(" select"),
				statusKeyStyle.Render("↵") + statusDescStyle.Render(" view"),
				statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
			}
		}
	case viewShell:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" run"),
			statusKeyStyle.Render("^U") + statusDescStyle.Render(" clear"),
			statusKeyStyle.Render("pgup/dn") + statusDescStyle.Render(" scroll"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" back"),
		}
	case viewMemory:
		keys = []string{
			statusKeyStyle.Render("^S") + statusDescStyle.Render(" save"),
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
				statusKeyStyle.Render("a") + statusDescStyle.Render(" toggle hidden"),
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
	case viewTemplatePicker:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" use"),
		}
		if m.templatePicker.projectName != "" {
			keys = append(keys,
				statusKeyStyle.Render("n")+statusDescStyle.Render(" new"),
				statusKeyStyle.Render("d")+statusDescStyle.Render(" delete"),
			)
		}
		keys = append(keys, statusKeyStyle.Render("esc")+statusDescStyle.Render(" back"))
	case viewCreateTemplate:
		keys = []string{
			statusKeyStyle.Render("↵") + statusDescStyle.Render(" create"),
			statusKeyStyle.Render("tab") + statusDescStyle.Render(" next"),
			statusKeyStyle.Render("esc") + statusDescStyle.Render(" cancel"),
		}
	}

	right := ""
	if m.statusMsg != "" {
		right = statusDescStyle.Render(m.statusMsg)
	} else if m.version != "" {
		right = statusDescStyle.Render("v" + m.version)
	}

	gapStyle := lipgloss.NewStyle().Background(colorPrimary)

	// Wrap keys into lines that fit the terminal width.
	var lines []string
	var currentLine string
	currentWidth := 0
	for _, k := range keys {
		kw := lipgloss.Width(k)
		if currentWidth > 0 && currentWidth+kw > m.width {
			// Fill remainder of line with background.
			gap := m.width - currentWidth
			if gap > 0 {
				currentLine += gapStyle.Render(fmt.Sprintf("%*s", gap, ""))
			}
			lines = append(lines, currentLine)
			currentLine = ""
			currentWidth = 0
		}
		currentLine += k
		currentWidth += kw
	}

	// Last line: add right-aligned status/version.
	if currentLine != "" {
		gap := m.width - currentWidth - lipgloss.Width(right)
		if gap < 0 {
			// No room for right side on this line.
			gap = m.width - currentWidth
			if gap > 0 {
				currentLine += gapStyle.Render(fmt.Sprintf("%*s", gap, ""))
			}
			lines = append(lines, currentLine)
			// Put right on its own line.
			if right != "" {
				rGap := m.width - lipgloss.Width(right)
				if rGap > 0 {
					lines = append(lines, gapStyle.Render(fmt.Sprintf("%*s", rGap, ""))+right)
				} else {
					lines = append(lines, right)
				}
			}
		} else {
			if gap > 0 {
				currentLine += gapStyle.Render(fmt.Sprintf("%*s", gap, ""))
			}
			currentLine += right
			lines = append(lines, currentLine)
		}
	}

	return strings.Join(lines, "\n")
}

// Run starts the TUI.
func Run(version string, opts ...UIOptions) error {
	initUILog()
	uilog("TUI starting, version=%s", version)
	var o UIOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	p := tea.NewProgram(New(version, o), tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
