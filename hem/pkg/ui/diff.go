package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
}

type diffLoadedMsg struct {
	diff string
	err  error
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

func (m diffModel) Update(msg tea.Msg) (diffModel, tea.Cmd) {
	switch msg := msg.(type) {
	case diffLoadedMsg:
		m.loading = false
		m.diff = msg.diff
		m.err = msg.err

	case tea.KeyMsg:
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

	// Render diff with colors.
	lines := strings.Split(m.diff, "\n")
	viewHeight := m.height - 4
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
