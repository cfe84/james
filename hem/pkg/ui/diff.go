package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type diffMode int

const (
	diffModeView diffMode = iota
	diffModeCommitMsg
)

type diffTab int

const (
	diffTabDiff diffTab = iota
	diffTabLog
	diffTabCommit // viewing a specific commit
)

// logEntry represents a parsed git log line with its commit hash.
type logEntry struct {
	line string // original line
	hash string // commit hash (empty for non-commit lines like graph connectors)
}

// diffModel displays a git diff for a session.
type diffModel struct {
	sessionID string
	diff      string
	gitLog    string
	branch    string
	tab       diffTab
	prevTab   diffTab // tab to return to when leaving commit view
	scroll    int
	logScroll int
	width     int
	height    int
	err       error
	loading   bool
	logLoading bool
	client    *client

	mode       diffMode
	commitMsg  string
	pushAfter  bool // if true, push after commit
	committing bool
	commitErr  error

	// Log selection.
	logEntries  []logEntry
	logCursor   int

	// Commit detail view.
	commitDetail  string
	commitHash    string
	commitScroll  int
	commitLoading bool
	commitErr2    error
}

type diffLoadedMsg struct {
	diff string
	err  error
}

type gitLogLoadedMsg struct {
	log string
	err error
}

type gitInfoLoadedMsg struct {
	branch string
	err    error
}

type diffCommitDoneMsg struct {
	pushed bool
	err    error
}

type diffPushDoneMsg struct {
	err error
}

type gitShowLoadedMsg struct {
	show string
	hash string
	err  error
}

func newDiffModel(c *client, sessionID string) diffModel {
	return diffModel{
		client:    c,
		sessionID: sessionID,
		loading:   true,
		logLoading: true,
	}
}

func (m diffModel) loadDiff() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		resp, err := m.client.send("diff", "session", sessionID)
		if err != nil {
			return diffLoadedMsg{err: err}
		}
		if resp.Status == "error" {
			return diffLoadedMsg{err: fmt.Errorf("%s", resp.Message)}
		}
		var result struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(resp.Data, &result); err != nil {
			return diffLoadedMsg{err: fmt.Errorf("parsing diff: %w", err)}
		}
		return diffLoadedMsg{diff: result.Message}
	}
}

func (m diffModel) loadGitLog() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		log, err := m.client.gitLog(sessionID)
		return gitLogLoadedMsg{log: log, err: err}
	}
}

func (m diffModel) loadGitInfo() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		branch, err := m.client.gitInfo(sessionID)
		return gitInfoLoadedMsg{branch: branch, err: err}
	}
}

func (m diffModel) loadGitShow(hash string) tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		show, err := m.client.gitShow(sessionID, hash)
		return gitShowLoadedMsg{show: show, hash: hash, err: err}
	}
}

func (m diffModel) doCommit() tea.Cmd {
	sessionID := m.sessionID
	msg := m.commitMsg
	push := m.pushAfter
	return func() tea.Msg {
		err := m.client.commitSession(sessionID, msg)
		if err != nil {
			return diffCommitDoneMsg{err: err}
		}
		if push {
			err = m.client.pushSession(sessionID)
			return diffCommitDoneMsg{pushed: true, err: err}
		}
		return diffCommitDoneMsg{}
	}
}

func (m diffModel) doPush() tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		err := m.client.pushSession(sessionID)
		return diffPushDoneMsg{err: err}
	}
}

// parseLogEntries extracts commit hashes from git log --oneline --graph output.
func parseLogEntries(gitLog string) []logEntry {
	lines := strings.Split(gitLog, "\n")
	entries := make([]logEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		hash := extractHash(line)
		entries = append(entries, logEntry{line: line, hash: hash})
	}
	return entries
}

// extractHash finds the commit hash in a git log --oneline --graph line.
// Lines look like: "* abc1234 msg", "| * abc1234 msg", "* abc1234 (HEAD -> main) msg"
func extractHash(line string) string {
	// Skip graph characters (*, |, /, \, space, _) to find the hash.
	i := 0
	for i < len(line) {
		c := line[i]
		if c == '*' || c == '|' || c == '/' || c == '\\' || c == ' ' || c == '_' {
			i++
		} else {
			break
		}
	}
	if i >= len(line) {
		return ""
	}
	// The hash is the next word.
	end := i
	for end < len(line) && line[end] != ' ' {
		end++
	}
	hash := line[i:end]
	// Validate it looks like a hex hash (at least 7 chars).
	if len(hash) >= 7 && isHex(hash) {
		return hash
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffLoadedMsg:
		m.loading = false
		m.diff = msg.diff
		m.err = msg.err

	case gitLogLoadedMsg:
		m.logLoading = false
		if msg.err == nil {
			m.gitLog = msg.log
			m.logEntries = parseLogEntries(msg.log)
			if m.logCursor >= len(m.logEntries) {
				m.logCursor = 0
			}
		}

	case gitInfoLoadedMsg:
		if msg.err == nil {
			m.branch = msg.branch
		}

	case gitShowLoadedMsg:
		m.commitLoading = false
		if msg.err != nil {
			m.commitErr2 = msg.err
		} else {
			m.commitDetail = msg.show
			m.commitHash = msg.hash
			m.commitScroll = 0
		}

	case diffCommitDoneMsg:
		m.committing = false
		if msg.err != nil {
			m.commitErr = msg.err
		} else {
			m.commitErr = nil
			m.mode = diffModeView
			m.commitMsg = ""
			// Reload diff after commit.
			m.loading = true
			m.logLoading = true
			return m, tea.Batch(m.loadDiff(), m.loadGitLog())
		}

	case diffPushDoneMsg:
		m.committing = false
		if msg.err != nil {
			m.commitErr = msg.err
		} else {
			m.commitErr = nil
		}

	case tea.KeyMsg:
		if m.mode == diffModeCommitMsg {
			return m.updateCommitInput(msg)
		}

		// Commit detail view.
		if m.tab == diffTabCommit {
			switch msg.String() {
			case "esc":
				m.tab = m.prevTab
				m.commitDetail = ""
				m.commitHash = ""
				m.commitErr2 = nil
			case "up", "k":
				if m.commitScroll > 0 {
					m.commitScroll--
				}
			case "down", "j":
				m.commitScroll++
			case "pgup":
				m.commitScroll -= 10
				if m.commitScroll < 0 {
					m.commitScroll = 0
				}
			case "pgdown":
				m.commitScroll += 10
			}
			return m, nil
		}

		switch msg.String() {
		case "tab":
			if m.tab == diffTabDiff {
				m.tab = diffTabLog
			} else {
				m.tab = diffTabDiff
			}
		case "up", "k":
			if m.tab == diffTabLog {
				if m.logCursor > 0 {
					m.logCursor--
				}
			} else {
				if m.scroll > 0 {
					m.scroll--
				}
			}
		case "down", "j":
			if m.tab == diffTabLog {
				if m.logCursor < len(m.logEntries)-1 {
					m.logCursor++
				}
			} else {
				m.scroll++
			}
		case "pgup":
			if m.tab == diffTabLog {
				m.logCursor -= 10
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll -= 10
				if m.scroll < 0 {
					m.scroll = 0
				}
			}
		case "pgdown":
			if m.tab == diffTabLog {
				m.logCursor += 10
				if m.logCursor >= len(m.logEntries) {
					m.logCursor = len(m.logEntries) - 1
				}
				if m.logCursor < 0 {
					m.logCursor = 0
				}
			} else {
				m.scroll += 10
			}
		case "enter":
			if m.tab == diffTabLog && len(m.logEntries) > 0 && m.logCursor < len(m.logEntries) {
				entry := m.logEntries[m.logCursor]
				if entry.hash != "" {
					m.prevTab = m.tab
					m.tab = diffTabCommit
					m.commitLoading = true
					m.commitErr2 = nil
					m.commitDetail = ""
					m.commitHash = entry.hash
					m.commitScroll = 0
					return m, m.loadGitShow(entry.hash)
				}
			}
		case "c":
			if m.tab == diffTabDiff && m.diff != "" {
				m.mode = diffModeCommitMsg
				m.pushAfter = false
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "C":
			if m.tab == diffTabDiff && m.diff != "" {
				m.mode = diffModeCommitMsg
				m.pushAfter = true
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "p":
			if !m.committing {
				m.committing = true
				m.commitErr = nil
				return m, m.doPush()
			}
		}
	}
	return m, nil
}

func (m diffModel) updateCommitInput(msg tea.KeyMsg) (diffModel, tea.Cmd) {
	if m.committing {
		return m, nil
	}
	switch msg.String() {
	case "enter":
		if strings.TrimSpace(m.commitMsg) != "" {
			m.committing = true
			return m, m.doCommit()
		}
	case "esc":
		m.mode = diffModeView
		m.commitMsg = ""
		m.commitErr = nil
	case "backspace":
		if len(m.commitMsg) > 0 {
			m.commitMsg = m.commitMsg[:len(m.commitMsg)-1]
		}
	case "ctrl+u":
		m.commitMsg = ""
	default:
		if msg.Type == tea.KeyRunes {
			m.commitMsg += string(msg.Runes)
		} else if msg.String() == " " {
			m.commitMsg += " "
		}
	}
	return m, nil
}

func (m diffModel) View() string {
	var b strings.Builder

	// Title with branch name.
	title := " Git "
	if m.branch != "" {
		title += fmt.Sprintf("(%s) ", m.branch)
	}
	title += truncate(m.sessionID, 20) + " "
	b.WriteString(titleStyle.Render(title))

	// Tab bar.
	diffLabel := " Diff "
	logLabel := " Log "
	if m.tab == diffTabCommit {
		// In commit detail view, show commit hash as active tab.
		diffLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(logLabel)
		commitLabel := fmt.Sprintf(" %s ", truncate(m.commitHash, 10))
		commitLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(commitLabel)
		b.WriteString("  " + diffLabel + " " + logLabel + " " + commitLabel)
	} else if m.tab == diffTabDiff {
		diffLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(logLabel)
		b.WriteString("  " + diffLabel + " " + logLabel)
	} else {
		diffLabel = lipgloss.NewStyle().Foreground(colorMuted).Render(diffLabel)
		logLabel = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(logLabel)
		b.WriteString("  " + diffLabel + " " + logLabel)
	}
	b.WriteString("\n")

	switch m.tab {
	case diffTabDiff:
		b.WriteString(m.viewDiff())
	case diffTabLog:
		b.WriteString(m.viewLog())
	case diffTabCommit:
		b.WriteString(m.viewCommit())
	}

	return b.String()
}

func (m diffModel) viewDiff() string {
	var b strings.Builder

	if m.loading {
		b.WriteString("\n  Loading diff...")
		return b.String()
	}
	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v", m.err))
		return b.String()
	}
	if m.diff == "" {
		b.WriteString("\n  No changes (working tree clean)")
		return b.String()
	}

	// Reserve space for commit input if active.
	commitHeight := 0
	if m.mode == diffModeCommitMsg {
		commitHeight = 3
		if m.commitErr != nil {
			commitHeight = 4
		}
	}

	// Render diff with colors.
	lines := strings.Split(m.diff, "\n")
	viewHeight := m.height - 5 - commitHeight
	if viewHeight < 1 {
		viewHeight = 20
	}

	// Clamp scroll.
	maxScroll := len(lines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	end := m.scroll + viewHeight
	if end > len(lines) {
		end = len(lines)
	}

	for i := m.scroll; i < end; i++ {
		line := lines[i]
		styled := colorDiffLine(line)
		b.WriteString(styled)
		b.WriteString("\n")
	}

	if len(lines) > viewHeight {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  line %d-%d of %d", m.scroll+1, end, len(lines))))
		b.WriteString("\n")
	}

	// Commit message input.
	if m.mode == diffModeCommitMsg {
		b.WriteString("\n")
		action := "Commit message"
		if m.pushAfter {
			action = "Commit+push message"
		}
		if m.committing {
			if m.pushAfter {
				b.WriteString("  Committing and pushing...")
			} else {
				b.WriteString("  Committing...")
			}
		} else {
			b.WriteString("  " + labelStyle.Render(action+":") + " " + fieldActiveStyle.Render(m.commitMsg+"█"))
		}
		if m.commitErr != nil {
			b.WriteString("\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Render(m.commitErr.Error()))
		}
	}

	return b.String()
}

func (m diffModel) viewLog() string {
	var b strings.Builder

	if m.logLoading {
		b.WriteString("\n  Loading git log...")
		return b.String()
	}
	if len(m.logEntries) == 0 {
		b.WriteString("\n  No commits")
		return b.String()
	}

	viewHeight := m.height - 5
	if viewHeight < 1 {
		viewHeight = 20
	}

	// Auto-scroll to keep cursor visible.
	if m.logCursor < m.logScroll {
		m.logScroll = m.logCursor
	}
	if m.logCursor >= m.logScroll+viewHeight {
		m.logScroll = m.logCursor - viewHeight + 1
	}

	maxScroll := len(m.logEntries) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.logScroll > maxScroll {
		m.logScroll = maxScroll
	}

	end := m.logScroll + viewHeight
	if end > len(m.logEntries) {
		end = len(m.logEntries)
	}

	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("#333333"))

	for i := m.logScroll; i < end; i++ {
		entry := m.logEntries[i]
		styled := colorLogLine(entry.line)
		if i == m.logCursor {
			// Render selected line with highlight background.
			styled = selectedStyle.Render(colorLogLineRaw(entry.line))
		}
		b.WriteString(styled)
		b.WriteString("\n")
	}

	if len(m.logEntries) > viewHeight {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  %d/%d  Enter=view commit", m.logCursor+1, len(m.logEntries))))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  Enter=view commit"))
		b.WriteString("\n")
	}

	return b.String()
}

func (m diffModel) viewCommit() string {
	var b strings.Builder

	if m.commitLoading {
		b.WriteString("\n  Loading commit " + m.commitHash + "...")
		return b.String()
	}
	if m.commitErr2 != nil {
		b.WriteString(fmt.Sprintf("\n  Error: %v", m.commitErr2))
		b.WriteString("\n\n  Press Esc to go back")
		return b.String()
	}
	if m.commitDetail == "" {
		b.WriteString("\n  No commit data")
		b.WriteString("\n\n  Press Esc to go back")
		return b.String()
	}

	lines := strings.Split(m.commitDetail, "\n")
	viewHeight := m.height - 5
	if viewHeight < 1 {
		viewHeight = 20
	}

	maxScroll := len(lines) - viewHeight
	if maxScroll < 0 {
		maxScroll = 0
	}
	if m.commitScroll > maxScroll {
		m.commitScroll = maxScroll
	}

	end := m.commitScroll + viewHeight
	if end > len(lines) {
		end = len(lines)
	}

	for i := m.commitScroll; i < end; i++ {
		line := lines[i]
		styled := colorDiffLine(line)
		b.WriteString(styled)
		b.WriteString("\n")
	}

	if len(lines) > viewHeight {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  line %d-%d of %d  Esc=back", m.commitScroll+1, end, len(lines))))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  Esc=back"))
		b.WriteString("\n")
	}

	return b.String()
}

var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	diffRemoveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
	diffHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	logGraphStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	logHashStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
	logDecorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
)

func colorDiffLine(line string) string {
	if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
		return diffHeaderStyle.Render(line)
	}
	if strings.HasPrefix(line, "+") {
		return diffAddStyle.Render(line)
	}
	if strings.HasPrefix(line, "-") {
		return diffRemoveStyle.Render(line)
	}
	if strings.HasPrefix(line, "@@") {
		return diffHunkStyle.Render(line)
	}
	if strings.HasPrefix(line, "diff ") {
		return diffHeaderStyle.Render(line)
	}
	return line
}

func colorLogLine(line string) string {
	// Color the graph characters and hash in git log --oneline --graph output.
	// Lines look like: "* abc1234 Some commit message" or "| * abc1234 msg"
	trimmed := strings.TrimLeft(line, " ")

	// Find where graph chars end and hash begins.
	graphEnd := 0
	for i, c := range line {
		if c == '*' || c == '|' || c == '/' || c == '\\' || c == ' ' || c == '_' {
			graphEnd = i + 1
		} else {
			break
		}
	}

	if graphEnd > 0 && graphEnd < len(line) {
		graph := logGraphStyle.Render(line[:graphEnd])
		rest := line[graphEnd:]
		// Try to color the hash (first word after graph).
		if idx := strings.Index(rest, " "); idx > 0 {
			hash := logHashStyle.Render(rest[:idx])
			msg := rest[idx:]
			// Color decorations like (HEAD -> main, origin/main).
			if strings.Contains(msg, "(") {
				parts := strings.SplitN(msg, "(", 2)
				if len(parts) == 2 {
					closeParen := strings.Index(parts[1], ")")
					if closeParen >= 0 {
						decor := logDecorStyle.Render("(" + parts[1][:closeParen] + ")")
						msg = parts[0] + decor + parts[1][closeParen+1:]
					}
				}
			}
			return graph + hash + msg
		}
		return graph + rest
	}

	if len(trimmed) > 0 && trimmed[0] == '*' {
		return logGraphStyle.Render(line)
	}
	return line
}

// colorLogLineRaw colors a log line for the selected (highlighted) row.
// Uses plain text so the background highlight can show through.
func colorLogLineRaw(line string) string {
	// For the selected line, just return the raw text without ANSI colors
	// so the background highlight works cleanly.
	return line
}
