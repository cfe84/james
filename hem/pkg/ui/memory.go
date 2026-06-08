package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// memMode is the sub-mode of the memory view.
type memMode int

const (
	memModeBrowse memMode = iota // tree list of nodes
	memModeEdit                  // create/replace a single node
	memModeSearch                // search box + results
)

// memoryModel is a view for browsing and editing the session's hierarchical
// memory tree. Memory is a tree of nodes (slash-delimited paths); each node has
// an optional title/description and a body. The view has three sub-modes:
// browse (the tree), edit (a per-node form), and search.
type memoryModel struct {
	sessionID string
	client    *client
	mode      memMode

	// Browse state.
	nodes  []memoryNodeView
	cursor int

	// Edit state.
	isNew    bool
	editPath string // path of the node being edited ("" until known)
	fields   []formField
	original []string
	fcursor  int

	// Search state.
	searchInput  textInput
	searchTerm   string
	results      []memoryNodeView
	resultCursor int
	searched     bool

	width         int
	height        int
	loading       bool
	saving        bool
	err           error
	confirmDelete bool
	statusMsg     string
}

type memoryLoadedMsg struct {
	nodes []memoryNodeView
	err   error
}

type memoryNodeLoadedMsg struct {
	node     *memoryNodeView
	children []memoryNodeView
	err      error
}

type memorySavedMsg struct {
	path string
	err  error
}

type memoryDeletedMsg struct {
	err error
}

type memorySearchedMsg struct {
	results []memoryNodeView
	err     error
}

func newMemoryModel(c *client, sessionID string) memoryModel {
	return memoryModel{
		client:      c,
		sessionID:   sessionID,
		mode:        memModeBrowse,
		loading:     true,
		searchInput: newTextInput(false),
	}
}

func (m memoryModel) loadMemory() tea.Cmd {
	sessionID := m.sessionID
	client := m.client
	return func() tea.Msg {
		nodes, err := client.loadMemoryTree(sessionID)
		return memoryLoadedMsg{nodes: nodes, err: err}
	}
}

func (m memoryModel) loadNode(path string) tea.Cmd {
	sessionID := m.sessionID
	client := m.client
	return func() tea.Msg {
		node, children, err := client.getMemoryNode(sessionID, path)
		return memoryNodeLoadedMsg{node: node, children: children, err: err}
	}
}

func (m memoryModel) saveNode() tea.Cmd {
	sessionID := m.sessionID
	client := m.client
	path := m.fields[0].value
	title := m.fields[1].value
	desc := m.fields[2].value
	body := m.fields[3].value
	return func() tea.Msg {
		err := client.saveMemoryNode(sessionID, path, title, desc, body)
		return memorySavedMsg{path: path, err: err}
	}
}

func (m memoryModel) deleteNode(path string, recursive bool) tea.Cmd {
	sessionID := m.sessionID
	client := m.client
	return func() tea.Msg {
		err := client.deleteMemoryNode(sessionID, path, recursive)
		return memoryDeletedMsg{err: err}
	}
}

func (m memoryModel) runSearch(query string) tea.Cmd {
	sessionID := m.sessionID
	client := m.client
	return func() tea.Msg {
		results, err := client.searchMemoryNodes(sessionID, query)
		return memorySearchedMsg{results: results, err: err}
	}
}

func (m memoryModel) selectedNode() *memoryNodeView {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return nil
	}
	return &m.nodes[m.cursor]
}

// newEditFields builds the per-node edit form. When path is non-empty and
// isNew is false, the Path field is locked (renaming is not supported — create
// a new node instead).
func newEditFields(path, title, description, body string, isNew bool) ([]formField, []string) {
	bodyInput := newTextInput(true)
	bodyInput.SetValue(body)
	fields := []formField{
		{label: "Path", flag: "path", value: path, cursorPos: len(path)},
		{label: "Title", flag: "--title", value: title, cursorPos: len(title)},
		{label: "Description", flag: "--description", value: description, cursorPos: len(description)},
		{label: "Body", flag: "body", value: body, cursorPos: len(body), input: &bodyInput},
	}
	original := make([]string, len(fields))
	for i, f := range fields {
		original[i] = f.value
	}
	return fields, original
}

func (m *memoryModel) openNew() {
	parent := ""
	if n := m.selectedNode(); n != nil {
		parent = n.Path + "/"
	}
	m.fields, m.original = newEditFields(parent, "", "", "", true)
	m.isNew = true
	m.editPath = ""
	m.fcursor = 0
	m.err = nil
	m.mode = memModeEdit
}

func (m memoryModel) Update(msg tea.Msg) (memoryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case memoryLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.nodes = msg.nodes
		if m.cursor >= len(m.nodes) {
			m.cursor = max(0, len(m.nodes)-1)
		}
		return m, nil

	case memoryNodeLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		if msg.node != nil {
			m.fields, m.original = newEditFields(msg.node.Path, msg.node.Title, msg.node.Description, msg.node.Body, false)
			m.isNew = false
			m.editPath = msg.node.Path
			m.fcursor = 3 // focus body
			m.mode = memModeEdit
		}
		return m, nil

	case memorySavedMsg:
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.statusMsg = "Saved " + msg.path
		m.mode = memModeBrowse
		m.loading = true
		return m, m.loadMemory()

	case memoryDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.statusMsg = "Deleted"
		m.loading = true
		return m, m.loadMemory()

	case memorySearchedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.results = msg.results
		m.resultCursor = 0
		m.searched = true
		return m, nil

	case tea.KeyMsg:
		if m.loading || m.saving {
			return m, nil
		}
		switch m.mode {
		case memModeBrowse:
			return m.updateBrowse(msg)
		case memModeEdit:
			return m.updateEdit(msg)
		case memModeSearch:
			return m.updateSearch(msg)
		}
	}
	return m, nil
}

func (m memoryModel) updateBrowse(msg tea.KeyMsg) (memoryModel, tea.Cmd) {
	wasConfirmingDelete := m.confirmDelete
	if msg.String() != "d" {
		m.confirmDelete = false
	}
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.nodes)-1 {
			m.cursor++
		}
	case "enter", "e":
		if n := m.selectedNode(); n != nil {
			m.loading = true
			return m, m.loadNode(n.Path)
		}
	case "n":
		m.openNew()
		return m, nil
	case "/":
		m.mode = memModeSearch
		m.searchInput.Reset()
		m.searched = false
		m.results = nil
		return m, nil
	case "r":
		m.loading = true
		return m, m.loadMemory()
	case "d":
		if n := m.selectedNode(); n != nil {
			if !wasConfirmingDelete {
				m.confirmDelete = true
				return m, nil
			}
			m.confirmDelete = false
			// Recursive delete so nodes with children can be removed from the UI.
			return m, m.deleteNode(n.Path, true)
		}
	}
	return m, nil
}

func (m memoryModel) updateEdit(msg tea.KeyMsg) (memoryModel, tea.Cmd) {
	field := &m.fields[m.fcursor]

	switch msg.String() {
	case "up":
		if m.fcursor > 0 {
			m.fcursor--
		}
		return m, nil
	case "down":
		if m.fcursor < len(m.fields)-1 {
			m.fcursor++
		}
		return m, nil
	case "tab":
		m.fcursor = (m.fcursor + 1) % len(m.fields)
		return m, nil
	case "ctrl+s":
		return m.submitEdit()
	}

	// Path field is read-only when editing an existing node.
	if m.fcursor == 0 && !m.isNew {
		return m, nil
	}

	if field.input != nil {
		switch msg.String() {
		case "ctrl+u":
			field.input.Reset()
			field.syncFromInput()
			return m, nil
		default:
			handled, _ := field.input.HandleKey(msg)
			if handled {
				field.syncFromInput()
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "enter":
		// Advance to next field on single-line fields.
		if m.fcursor < len(m.fields)-1 {
			m.fcursor++
		}
	case "backspace":
		if len(field.value) > 0 {
			field.value = field.value[:len(field.value)-1]
			field.cursorPos = len(field.value)
		}
	case "ctrl+u":
		field.value = ""
		field.cursorPos = 0
	default:
		if msg.Type == tea.KeyRunes {
			field.value += string(msg.Runes)
			field.cursorPos = len(field.value)
		} else if msg.Type == tea.KeySpace {
			field.value += " "
			field.cursorPos = len(field.value)
		}
	}
	return m, nil
}

func (m memoryModel) submitEdit() (memoryModel, tea.Cmd) {
	for i := range m.fields {
		m.fields[i].syncFromInput()
	}
	if strings.TrimSpace(m.fields[0].value) == "" {
		m.err = fmt.Errorf("path is required")
		return m, nil
	}
	m.saving = true
	m.err = nil
	return m, m.saveNode()
}

func (m memoryModel) updateSearch(msg tea.KeyMsg) (memoryModel, tea.Cmd) {
	switch msg.String() {
	case "up":
		if m.resultCursor > 0 {
			m.resultCursor--
		}
		return m, nil
	case "down":
		if m.resultCursor < len(m.results)-1 {
			m.resultCursor++
		}
		return m, nil
	case "enter":
		// If results are present, open the selected one; otherwise run search.
		if m.searched && len(m.results) > 0 {
			path := m.results[m.resultCursor].Path
			m.mode = memModeBrowse
			m.loading = true
			return m, m.loadNode(path)
		}
		query := strings.TrimSpace(m.searchInput.Value())
		if query == "" {
			return m, nil
		}
		m.searchTerm = query
		return m, m.runSearch(query)
	case "ctrl+u":
		m.searchInput.Reset()
		m.searched = false
		m.results = nil
		return m, nil
	default:
		_, submitted := m.searchInput.HandleKey(msg)
		if submitted {
			query := strings.TrimSpace(m.searchInput.Value())
			if query != "" {
				m.searchTerm = query
				return m, m.runSearch(query)
			}
		}
	}
	return m, nil
}

func (m memoryModel) View() string {
	if m.loading {
		return "\n  Loading memory..."
	}
	switch m.mode {
	case memModeEdit:
		return m.viewEdit()
	case memModeSearch:
		return m.viewSearch()
	default:
		return m.viewBrowse()
	}
}

func (m memoryModel) viewBrowse() string {
	var b strings.Builder
	tStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	b.WriteString(tStyle.Render(" Session Memory"))
	b.WriteString("\n")

	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	b.WriteString(hintStyle.Render("  Enter/e edit · n new · d delete · / search · r refresh · Esc back"))
	b.WriteString("\n")
	if m.statusMsg != "" {
		b.WriteString(hintStyle.Render("  " + m.statusMsg))
		b.WriteString("\n")
	}
	if m.err != nil {
		errStyle := lipgloss.NewStyle().Foreground(colorDanger)
		b.WriteString(errStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(m.nodes) == 0 {
		b.WriteString(hintStyle.Render("  (empty — press n to create the first memory node)"))
		return b.String()
	}

	maxRows := m.height - 6
	if maxRows < 1 {
		maxRows = 10
	}
	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.nodes) {
		end = len(m.nodes)
	}

	for i := start; i < end; i++ {
		n := m.nodes[i]
		depth := strings.Count(n.Path, "/")
		leaf := n.Path
		if idx := strings.LastIndex(n.Path, "/"); idx >= 0 {
			leaf = n.Path[idx+1:]
		}
		label := strings.Repeat("  ", depth) + leaf
		desc := n.Description
		if desc == "" {
			desc = n.Title
		}
		line := fmt.Sprintf("  %-32s %s", truncate(label, 32), truncate(desc, 44))
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

	if len(m.nodes) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.nodes))))
		b.WriteString("\n")
	}

	if m.confirmDelete {
		n := m.selectedNode()
		path := ""
		if n != nil {
			path = n.Path
		}
		warnStyle := lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
		hStyle := lipgloss.NewStyle().Foreground(colorMuted)
		b.WriteString("\n  " + warnStyle.Render(fmt.Sprintf("Delete %q and all descendants?", path)) +
			"  " + hStyle.Render("press d again to confirm · any other key cancels"))
		b.WriteString("\n")
	}

	return b.String()
}

func (m memoryModel) viewEdit() string {
	var b strings.Builder

	title := " New Memory Node "
	if !m.isNew {
		title = fmt.Sprintf(" Edit Node: %s ", truncate(m.editPath, 28))
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	labels := make([]string, len(m.fields))
	for i, f := range m.fields {
		labels[i] = f.label
	}
	labelW := formLabelWidth(labels)
	lStyle := labelStyle.Width(labelW)
	valueIndent := labelW + 3
	maxValueWidth := m.width - valueIndent - 2
	if maxValueWidth < 20 {
		maxValueWidth = 20
	}

	for i, f := range m.fields {
		label := lStyle.Render(truncateDisplay(f.label+":", labelW))
		locked := i == 0 && !m.isNew

		var value string
		switch {
		case locked:
			value = fieldInactiveStyle.Render(f.value + "  (locked)")
		case i == m.fcursor:
			if f.input != nil {
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
				value = fieldActiveStyle.Render(f.value + "█")
			}
		default:
			if f.value == "" {
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
				value = fieldInactiveStyle.Render(f.value)
			}
		}
		b.WriteString("  " + label + " " + value + "\n")
	}

	b.WriteString("\n")
	if m.saving {
		b.WriteString("  Saving...")
	} else {
		b.WriteString(statusDescStyle.Render(" Ctrl+S ") + " save  " +
			statusDescStyle.Render(" Tab ") + " next field  " +
			statusDescStyle.Render(" Esc ") + " back  " +
			statusDescStyle.Render(" Ctrl+U ") + " clear  " +
			statusDescStyle.Render(" Ctrl+J ") + " newline")
	}
	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}

func (m memoryModel) viewSearch() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" Search Memory "))
	b.WriteString("\n\n")

	b.WriteString("  Query: " + m.searchInput.Render() + "█")
	b.WriteString("\n\n")

	hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.err)))
		b.WriteString("\n")
	}

	if m.searched {
		if len(m.results) == 0 {
			b.WriteString(hintStyle.Render(fmt.Sprintf("  No matches for %q.", m.searchTerm)))
			b.WriteString("\n")
		} else {
			for i, r := range m.results {
				desc := r.Description
				if desc == "" {
					desc = r.Title
				}
				line := fmt.Sprintf("  %-32s %s", truncate(r.Path, 32), truncate(desc, 44))
				if i == m.resultCursor {
					if m.width > 0 && lipgloss.Width(line) < m.width {
						line += strings.Repeat(" ", m.width-lipgloss.Width(line))
					}
					b.WriteString(sessionSelectedStyle.Render(line))
				} else {
					b.WriteString(sessionNormalStyle.Render(line))
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	b.WriteString(hintStyle.Render("  Enter search / open · ↑/↓ select · Esc back"))
	return b.String()
}
