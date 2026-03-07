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

// diffModel displays a git diff for a session.
type diffModel struct {
	sessionID string
	diff      string
	scroll    int
	width     int
	height    int
	err       error
	loading   bool
	client    *client

	mode       diffMode
	commitMsg  string
	pushAfter  bool // if true, push after commit
	committing bool
	commitErr  error
}

type diffLoadedMsg struct {
	diff string
	err  error
}

type diffCommitDoneMsg struct {
	pushed bool
	err    error
}

type diffPushDoneMsg struct {
	err error
}

func newDiffModel(c *client, sessionID string) diffModel {
	return diffModel{
		client:    c,
		sessionID: sessionID,
		loading:   true,
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

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffLoadedMsg:
		m.loading = false
		m.diff = msg.diff
		m.err = msg.err

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
			return m, m.loadDiff()
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
		switch msg.String() {
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			m.scroll++
		case "pgup":
			m.scroll -= 10
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "pgdown":
			m.scroll += 10
		case "c":
			if m.diff != "" {
				m.mode = diffModeCommitMsg
				m.pushAfter = false
				m.commitMsg = ""
				m.commitErr = nil
			}
		case "C":
			if m.diff != "" {
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

	title := fmt.Sprintf(" Git Diff: %s ", truncate(m.sessionID, 20))
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

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
	viewHeight := m.height - 4 - commitHeight
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

var (
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	diffRemoveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
	diffHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
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
