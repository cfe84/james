package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func defaultWizardPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

const (
	wizardStepMoneypenny = 0
	wizardStepPath       = 1
	wizardStepForm       = 2
)

// wizardModel is a 3-step wizard for creating a new session or project.
type wizardModel struct {
	step   int
	width  int
	height int
	client *client
	err    error

	// Step 1: moneypenny selection
	moneypennies []moneypennyInfo
	mpCursor     int
	mpLoading    bool

	// Step 2: path browser
	currentPath string
	dirEntries  []dirEntry
	pathCursor  int
	pathLoading bool
	showHidden  bool // toggle with 'a' to show/hide hidden directories

	// Step 3: form (reuses createModel fields minus moneypenny/path)
	fields   []formField
	fCursor  int
	creating bool

	// Pre-fill from project context
	async       bool
	projectName string

	// Project mode
	forProject bool

	// Selections
	selectedMP   string
	selectedPath string

	// Model cache: agent name → []string options
	modelCache map[string][]string
	lastAgent  string // tracks agent value to detect changes
}

type wizardMPLoadedMsg struct {
	moneypennies []moneypennyInfo
	err          error
}

type wizardProjectsLoadedMsg struct {
	projects []projectInfo
}

type wizardDirLoadedMsg struct {
	entries []dirEntry
	err     error
}

type wizardModelsLoadedMsg struct {
	models []string // option names including "" for default
	agent  string
}

func newWizardModel(c *client) wizardModel {
	spInput := newTextInput(true)
	return wizardModel{
		step:        wizardStepMoneypenny,
		client:      c,
		async:       true, // TUI always creates async; polling is done by chat/dashboard
		mpLoading:   true,
		currentPath: defaultWizardPath(),
		fields: []formField{
			{label: "Prompt", flag: "", value: ""},
			{label: "Name", flag: "--name", value: ""},
			{label: "Project", flag: "--project", value: ""},
			{label: "Agent", flag: "--agent", value: "", options: []string{"", "claude", "copilot"}},
			{label: "Model", flag: "--model", value: "", options: []string{""}},
			{label: "Effort", flag: "--effort", value: "", options: []string{"", "low", "medium", "high"}},
			{label: "System Prompt", flag: "--system-prompt", value: "", input: &spInput},
			{label: "License to Kill", flag: "--yolo", isBool: true, value: "true"},
			{label: "Gadgets (James tooling)", flag: "--gadgets", isBool: true, value: "false"},
		},
	}
}

func newProjectWizardModel(c *client) wizardModel {
	return wizardModel{
		step:        wizardStepMoneypenny,
		client:      c,
		mpLoading:   true,
		currentPath: defaultWizardPath(),
		forProject:  true,
		fields: []formField{
			{label: "Name", flag: "--name", value: ""},
			{label: "Agent", flag: "--agent", value: "", options: []string{"", "claude", "copilot"}},
			{label: "System Prompt", flag: "--system-prompt", value: ""},
		},
	}
}

type wizardProjectLoadedMsg struct {
	project *projectDetail
	err     error
}

func newWizardModelForProject(c *client, projectName string) wizardModel {
	m := newWizardModel(c)
	m.async = true
	m.projectName = projectName
	for i := range m.fields {
		if m.fields[i].flag == "--project" {
			m.fields[i].value = projectName
			m.fields[i].cursorPos = len(projectName)
		}
		if m.fields[i].flag == "--name" {
			m.fields[i].value = projectName
			m.fields[i].cursorPos = len(projectName)
		}
	}
	return m
}

func (m wizardModel) loadProjectDetails() tea.Cmd {
	name := m.projectName
	return func() tea.Msg {
		p, err := m.client.showProject(name)
		return wizardProjectLoadedMsg{project: p, err: err}
	}
}

func (m wizardModel) loadMoneypennies() tea.Cmd {
	return func() tea.Msg {
		mps, err := m.client.listMoneypennies()
		return wizardMPLoadedMsg{moneypennies: mps, err: err}
	}
}

func (m wizardModel) loadProjects() tea.Cmd {
	return func() tea.Msg {
		projects, _ := m.client.listProjects("")
		return wizardProjectsLoadedMsg{projects: projects}
	}
}

func (m wizardModel) loadDirectory() tea.Cmd {
	mp := m.selectedMP
	path := m.currentPath
	showHidden := m.showHidden
	return func() tea.Msg {
		entries, err := m.client.listDirectory(mp, path, showHidden)
		return wizardDirLoadedMsg{entries: entries, err: err}
	}
}

func (m wizardModel) loadModels(agent string) tea.Cmd {
	mp := m.selectedMP
	return func() tea.Msg {
		models, _ := m.client.listModels(mp, agent)
		if models == nil {
			models = []string{""}
		}
		return wizardModelsLoadedMsg{models: models, agent: agent}
	}
}

func (m wizardModel) createProject() tea.Cmd {
	return func() tea.Msg {
		var args []string
		args = append(args, "-m", m.selectedMP)
		args = append(args, "--path", m.selectedPath)

		for _, f := range m.fields {
			if f.value == "" {
				continue
			}
			args = append(args, f.flag, f.value)
		}
		err := m.client.createProject(args)
		return projectCreatedMsg{err: err}
	}
}

func (m wizardModel) createSession() tea.Cmd {
	return func() tea.Msg {
		var args []string
		args = append(args, "-m", m.selectedMP)
		args = append(args, "--path", m.selectedPath)

		prompt := ""
		for _, f := range m.fields {
			if f.flag == "" {
				prompt = f.value
				continue
			}
			if f.value == "" || (f.isBool && f.value == "false") {
				continue
			}
			if f.isBool {
				args = append(args, f.flag)
			} else {
				args = append(args, f.flag, f.value)
			}
		}
		if m.async {
			args = append(args, "--async")
		}
		args = append(args, prompt)
		id, resp, err := m.client.createSession(args)
		return sessionCreatedMsg{sessionID: id, response: resp, err: err}
	}
}

func (m wizardModel) Init() tea.Cmd {
	if m.projectName != "" {
		return tea.Batch(m.loadProjectDetails(), m.loadProjects())
	}
	if m.forProject {
		return m.loadMoneypennies()
	}
	return tea.Batch(m.loadMoneypennies(), m.loadProjects())
}

func (m wizardModel) Update(msg tea.Msg) (wizardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case wizardMPLoadedMsg:
		m.mpLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.moneypennies = msg.moneypennies
		// Pre-select the default moneypenny.
		for i, mp := range m.moneypennies {
			if mp.IsDefault {
				m.mpCursor = i
				break
			}
		}

	case wizardProjectsLoadedMsg:
		// Populate the Project selector options.
		options := []string{""}
		for _, p := range msg.projects {
			options = append(options, p.Name)
		}
		for i := range m.fields {
			if m.fields[i].flag == "--project" {
				m.fields[i].options = options
				break
			}
		}
		return m, nil

	case wizardProjectLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		p := msg.project
		m.selectedMP = p.Moneypenny
		// Use first path from project's JSON array.
		var paths []string
		if err := json.Unmarshal([]byte(p.Paths), &paths); err == nil && len(paths) > 0 {
			m.selectedPath = paths[0]
		}
		// Pre-fill agent and system prompt from project defaults.
		for i := range m.fields {
			if m.fields[i].flag == "--agent" && p.DefaultAgent != "" && m.fields[i].value == "" {
				m.fields[i].value = p.DefaultAgent
				m.fields[i].cursorPos = len(p.DefaultAgent)
			}
			if m.fields[i].flag == "--system-prompt" && p.DefaultSystemPrompt != "" && m.fields[i].value == "" {
				m.fields[i].value = p.DefaultSystemPrompt
				m.fields[i].cursorPos = len(p.DefaultSystemPrompt)
				m.fields[i].syncToInput()
			}
		}
		m.step = wizardStepForm
		return m, m.loadModelsIfNeeded()

	case wizardDirLoadedMsg:
		m.pathLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Sort: directories first, then files, alphabetically.
		entries := msg.entries
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Name < entries[j].Name
		})
		m.dirEntries = entries
		m.pathCursor = 0

	case wizardModelsLoadedMsg:
		if m.modelCache == nil {
			m.modelCache = make(map[string][]string)
		}
		m.modelCache[msg.agent] = msg.models
		// Update the Model field options if agent still matches.
		for i := range m.fields {
			if m.fields[i].flag == "--model" {
				agentVal := m.currentAgent()
				if agentVal == "" {
					agentVal = "claude"
				}
				if agentVal == msg.agent {
					m.fields[i].options = msg.models
					// Reset model value if it's not in the new options.
					found := false
					for _, opt := range msg.models {
						if opt == m.fields[i].value {
							found = true
							break
						}
					}
					if !found {
						m.fields[i].value = ""
					}
				}
				break
			}
		}
		return m, nil

	case tea.KeyMsg:
		m.err = nil
		switch m.step {
		case wizardStepMoneypenny:
			return m.updateMPStep(msg)
		case wizardStepPath:
			return m.updatePathStep(msg)
		case wizardStepForm:
			return m.updateFormStep(msg)
		}
	}
	return m, nil
}

func (m wizardModel) updateMPStep(msg tea.KeyMsg) (wizardModel, tea.Cmd) {
	if m.mpLoading {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.mpCursor > 0 {
			m.mpCursor--
		}
	case "down", "j":
		if m.mpCursor < len(m.moneypennies)-1 {
			m.mpCursor++
		}
	case "enter":
		if len(m.moneypennies) > 0 {
			m.selectedMP = m.moneypennies[m.mpCursor].Name
			m.step = wizardStepPath
			m.pathLoading = true
			return m, m.loadDirectory()
		}
	}
	return m, nil
}

func (m wizardModel) updatePathStep(msg tea.KeyMsg) (wizardModel, tea.Cmd) {
	if m.pathLoading {
		return m, nil
	}

	// Filter to only directories for navigation.
	dirs := m.visibleDirs()

	switch msg.String() {
	case "up", "k":
		if m.pathCursor > 0 {
			m.pathCursor--
		}
	case "down", "j":
		if m.pathCursor < len(dirs) {
			m.pathCursor++
		}
	case "enter":
		if m.pathCursor == 0 {
			// ".." — go up.
			parent := filepath.Dir(m.currentPath)
			if parent != m.currentPath {
				m.currentPath = parent
				m.pathLoading = true
				return m, m.loadDirectory()
			}
		} else {
			idx := m.pathCursor - 1
			if idx < len(dirs) {
				m.currentPath = filepath.Join(m.currentPath, dirs[idx].Name)
				m.pathLoading = true
				m.pathCursor = 0
				return m, m.loadDirectory()
			}
		}
	case "tab":
		// Confirm current path.
		m.selectedPath = m.currentPath
		m.step = wizardStepForm
		return m, m.loadModelsIfNeeded()
	case "backspace":
		// Go up one level.
		parent := filepath.Dir(m.currentPath)
		if parent != m.currentPath {
			m.currentPath = parent
			m.pathLoading = true
			return m, m.loadDirectory()
		}
	case "a":
		// Toggle show/hide hidden directories.
		m.showHidden = !m.showHidden
		m.pathLoading = true
		m.pathCursor = 0
		return m, m.loadDirectory()
	}
	return m, nil
}

func (m wizardModel) updateFormStep(msg tea.KeyMsg) (wizardModel, tea.Cmd) {
	if m.creating {
		return m, nil
	}
	field := &m.fields[m.fCursor]

	// Navigation keys handled before delegating to textInput.
	switch msg.String() {
	case "up":
		if m.fCursor > 0 {
			m.fCursor--
		}
		return m, nil
	case "down":
		if m.fCursor < len(m.fields)-1 {
			m.fCursor++
		}
		return m, nil
	case "tab":
		m.fCursor = (m.fCursor + 1) % len(m.fields)
		return m, nil
	}

	// Delegate to textInput if the field has one.
	if field.input != nil {
		submitForm := func() (wizardModel, tea.Cmd) {
			field.syncFromInput()
			required := m.fields[0].value
			if strings.TrimSpace(required) != "" {
				m.creating = true
				if m.forProject {
					return m, m.createProject()
				}
				return m, m.createSession()
			}
			return m, nil
		}
		switch msg.String() {
		case "enter":
			return submitForm()
		case "ctrl+u":
			field.input.Reset()
			field.syncFromInput()
			return m, nil
		default:
			handled, submitted := field.input.HandleKey(msg)
			if submitted {
				return submitForm()
			}
			if handled {
				field.syncFromInput()
			}
		}
		return m, nil
	}

	// Standard field handling for fields without textInput.
	switch msg.String() {
	case "enter":
		required := m.fields[0].value
		if strings.TrimSpace(required) != "" {
			m.creating = true
			if m.forProject {
				return m, m.createProject()
			}
			return m, m.createSession()
		}
	case "backspace":
		if !field.isBool && field.options == nil && field.cursorPos > 0 {
			_, size := utf8.DecodeLastRuneInString(field.value[:field.cursorPos])
			field.value = field.value[:field.cursorPos-size] + field.value[field.cursorPos:]
			field.cursorPos -= size
		}
	case "delete":
		if !field.isBool && field.options == nil && field.cursorPos < len(field.value) {
			_, size := utf8.DecodeRuneInString(field.value[field.cursorPos:])
			field.value = field.value[:field.cursorPos] + field.value[field.cursorPos+size:]
		}
	case "ctrl+u":
		if !field.isBool && field.options == nil {
			field.value = ""
			field.cursorPos = 0
		}
	case "left":
		if field.options != nil {
			cycleFieldOptionsBack(field)
		} else if !field.isBool && field.cursorPos > 0 {
			_, size := utf8.DecodeLastRuneInString(field.value[:field.cursorPos])
			field.cursorPos -= size
		}
	case "right":
		if field.options != nil {
			cycleFieldOptions(field)
		} else if !field.isBool && field.cursorPos < len(field.value) {
			_, size := utf8.DecodeRuneInString(field.value[field.cursorPos:])
			field.cursorPos += size
		}
	case "alt+left", "ctrl+b":
		if !field.isBool && field.options == nil {
			field.cursorPos = wordLeft(field.value, field.cursorPos)
		}
	case "alt+right", "ctrl+f":
		if !field.isBool && field.options == nil {
			field.cursorPos = wordRight(field.value, field.cursorPos)
		}
	case "home":
		if !field.isBool && field.options == nil {
			field.cursorPos = 0
		}
	case "end":
		if !field.isBool && field.options == nil {
			field.cursorPos = len(field.value)
		}
	case " ":
		if field.options != nil {
			cycleFieldOptions(field)
		} else if field.isBool {
			if field.value == "true" {
				field.value = "false"
			} else {
				field.value = "true"
			}
		} else {
			field.value = field.value[:field.cursorPos] + " " + field.value[field.cursorPos:]
			field.cursorPos++
		}
	default:
		if !field.isBool && field.options == nil {
			if msg.Type == tea.KeyRunes {
				s := string(msg.Runes)
				field.value = field.value[:field.cursorPos] + s + field.value[field.cursorPos:]
				field.cursorPos += len(s)
			}
		}
	}

	// Detect agent change and reload models.
	agent := m.currentAgent()
	if agent == "" {
		agent = "claude"
	}
	if agent != m.lastAgent {
		return m, m.loadModelsIfNeeded()
	}
	return m, nil
}

// currentAgent returns the current value of the Agent field.
func (m wizardModel) currentAgent() string {
	for _, f := range m.fields {
		if f.flag == "--agent" {
			return f.value
		}
	}
	return ""
}

// loadModelsIfNeeded returns a tea.Cmd to load models for the current agent,
// using the cache if available.
func (m *wizardModel) loadModelsIfNeeded() tea.Cmd {
	agent := m.currentAgent()
	if agent == "" {
		agent = "claude"
	}
	m.lastAgent = agent

	if m.modelCache != nil {
		if cached, ok := m.modelCache[agent]; ok {
			// Already cached — set models directly.
			for i := range m.fields {
				if m.fields[i].flag == "--model" {
					m.fields[i].options = cached
					break
				}
			}
			return nil
		}
	}
	return m.loadModels(agent)
}

// wrapText breaks text into lines of at most maxWidth characters.
// Tries to break at word boundaries when possible.
func wrapText(text string, maxWidth int) []string {
	if maxWidth <= 0 || len(text) <= maxWidth {
		return []string{text}
	}
	var lines []string
	for len(text) > 0 {
		if len(text) <= maxWidth {
			lines = append(lines, text)
			break
		}
		// Find last space within maxWidth.
		cut := maxWidth
		if idx := strings.LastIndex(text[:maxWidth], " "); idx > 0 {
			cut = idx + 1
		}
		lines = append(lines, text[:cut])
		text = text[cut:]
	}
	return lines
}

// visibleDirs returns only directory entries.
func (m wizardModel) visibleDirs() []dirEntry {
	var dirs []dirEntry
	for _, e := range m.dirEntries {
		if e.IsDir {
			dirs = append(dirs, e)
		}
	}
	return dirs
}

func (m wizardModel) View() string {
	var b strings.Builder

	// Step indicator.
	steps := []string{"Moneypenny", "Path", "Details"}
	var stepParts []string
	for i, name := range steps {
		n := fmt.Sprintf("%d. %s", i+1, name)
		if i == m.step {
			stepParts = append(stepParts, lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(n))
		} else if i < m.step {
			stepParts = append(stepParts, lipgloss.NewStyle().Foreground(colorSuccess).Render(n))
		} else {
			stepParts = append(stepParts, lipgloss.NewStyle().Foreground(colorMuted).Render(n))
		}
	}
	title := " New Agent "
	if m.forProject {
		title = " New Project "
	}
	b.WriteString(titleStyle.Render(title) + "  " + strings.Join(stepParts, " > "))
	b.WriteString("\n\n")

	switch m.step {
	case wizardStepMoneypenny:
		b.WriteString(m.viewMPStep())
	case wizardStepPath:
		b.WriteString(m.viewPathStep())
	case wizardStepForm:
		b.WriteString(m.viewFormStep())
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("Error: %v", m.err)))
	}

	return b.String()
}

func (m wizardModel) viewMPStep() string {
	var b strings.Builder

	if m.mpLoading {
		b.WriteString("  Loading moneypennies...\n")
		return b.String()
	}

	if len(m.moneypennies) == 0 {
		b.WriteString("  No moneypennies registered. Add one first.\n")
		return b.String()
	}

	b.WriteString("  Select a moneypenny:\n\n")
	maxVisible := m.height - 8
	if maxVisible < 5 {
		maxVisible = 5
	}
	start := 0
	if m.mpCursor >= maxVisible {
		start = m.mpCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.moneypennies) {
		end = len(m.moneypennies)
	}

	for i := start; i < end; i++ {
		mp := m.moneypennies[i]
		name := mp.Name
		if mp.IsDefault {
			name += " *"
		}
		info := lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" (%s: %s)", mp.Type, mp.Address))
		line := fmt.Sprintf("  %-20s%s", name, info)
		if i == m.mpCursor {
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (m wizardModel) viewPathStep() string {
	var b strings.Builder

	pathLabel := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(m.currentPath)
	mpLabel := lipgloss.NewStyle().Foreground(colorMuted).Render(m.selectedMP)
	b.WriteString(fmt.Sprintf("  %s  on %s", pathLabel, mpLabel))
	if m.showHidden {
		hiddenLabel := lipgloss.NewStyle().Foreground(colorMuted).Render("  [showing hidden]")
		b.WriteString(hiddenLabel)
	}
	b.WriteString("\n\n")

	if m.pathLoading {
		b.WriteString("  Loading...\n")
		return b.String()
	}

	dirs := m.visibleDirs()

	maxVisible := m.height - 10
	if maxVisible < 5 {
		maxVisible = 5
	}

	// Item 0 is "..", rest are dirs.
	totalItems := len(dirs) + 1
	start := 0
	if m.pathCursor >= maxVisible {
		start = m.pathCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > totalItems {
		end = totalItems
	}

	for i := start; i < end; i++ {
		var line string
		if i == 0 {
			line = "  ../"
		} else {
			line = fmt.Sprintf("  %s/", dirs[i-1].Name)
		}
		if i == m.pathCursor {
			if m.width > 0 && lipgloss.Width(line) < m.width {
				line += strings.Repeat(" ", m.width-lipgloss.Width(line))
			}
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}

	if totalItems > maxVisible {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  %d/%d directories", m.pathCursor+1, totalItems)))
		b.WriteString("\n")
	}

	return b.String()
}

func (m wizardModel) viewFormStep() string {
	var b strings.Builder

	// Show selections from previous steps.
	mpLabel := lipgloss.NewStyle().Foreground(colorMuted).Render("Moneypenny:")
	pathLabel := lipgloss.NewStyle().Foreground(colorMuted).Render("Path:")
	b.WriteString(fmt.Sprintf("  %s %s    %s %s\n\n",
		mpLabel, lipgloss.NewStyle().Foreground(colorSuccess).Render(m.selectedMP),
		pathLabel, lipgloss.NewStyle().Foreground(colorSuccess).Render(m.selectedPath)))

	// labelWidth (24) + indent (2) + space (1) = 27 chars before value.
	const valueIndent = 27
	maxValueWidth := m.width - valueIndent - 2
	if maxValueWidth < 20 {
		maxValueWidth = 20
	}

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")
		var value string
		if i == m.fCursor {
			if f.options != nil {
				display := f.value
				if display == "" {
					display = "(none)"
				}
				value = fieldActiveStyle.Render("◀ " + display + " ▶")
			} else if f.isBool {
				if f.value == "true" {
					value = fieldActiveStyle.Render("[x] " + f.value)
				} else {
					value = fieldActiveStyle.Render("[ ] " + f.value)
				}
			} else if f.input != nil {
				lines := f.input.RenderWrapped(maxValueWidth, valueIndent)
				var parts []string
				for j, line := range lines {
					rendered := fieldActiveStyle.Render(line)
					if j > 0 {
						rendered = strings.Repeat(" ", valueIndent) + rendered
					}
					parts = append(parts, rendered)
				}
				value = strings.Join(parts, "\n")
			} else {
				text := f.value[:f.cursorPos] + "\u2588" + f.value[f.cursorPos:]
				lines := wrapText(text, maxValueWidth)
				var parts []string
				for j, line := range lines {
					rendered := fieldActiveStyle.Render(line)
					if j > 0 {
						rendered = strings.Repeat(" ", valueIndent) + rendered
					}
					parts = append(parts, rendered)
				}
				value = strings.Join(parts, "\n")
			}
		} else {
			if f.options != nil {
				if f.value == "" {
					value = fieldInactiveStyle.Render("(none)")
				} else {
					value = fieldInactiveStyle.Render(f.value)
				}
			} else if f.isBool {
				value = fieldInactiveStyle.Render(f.value)
			} else if f.value == "" {
				value = fieldInactiveStyle.Render("(empty)")
			} else if f.input != nil {
				segments := splitLines(f.value)
				var allLines []string
				for _, seg := range segments {
					wrapped := wrapText(seg, maxValueWidth)
					if len(wrapped) == 0 {
						allLines = append(allLines, "")
					} else {
						allLines = append(allLines, wrapped...)
					}
				}
				var parts []string
				for j, line := range allLines {
					rendered := fieldInactiveStyle.Render(line)
					if j > 0 {
						rendered = strings.Repeat(" ", valueIndent) + rendered
					}
					parts = append(parts, rendered)
				}
				value = strings.Join(parts, "\n")
			} else {
				lines := wrapText(f.value, maxValueWidth)
				var parts []string
				for j, line := range lines {
					rendered := fieldInactiveStyle.Render(line)
					if j > 0 {
						rendered = strings.Repeat(" ", valueIndent) + rendered
					}
					parts = append(parts, rendered)
				}
				value = strings.Join(parts, "\n")
			}
		}
		b.WriteString("  " + label + " " + value + "\n")
	}

	if m.creating {
		if m.forProject {
			b.WriteString("\n  Creating project...")
		} else {
			b.WriteString("\n  Creating session...")
		}
	}

	return b.String()
}
