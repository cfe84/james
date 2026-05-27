package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// summaryMode describes whether the summary view is showing the summary
// itself or a modal asking where to save it.
type summaryMode int

const (
	summaryModeView summaryMode = iota
	summaryModeSavePath
)

// summaryModel displays the result of `hem summarize session` in a
// scrollable view. The user can save the summary to a local file via `s`.
type summaryModel struct {
	client      *client
	sessionID   string
	sessionName string

	loading bool
	summary string
	err     error

	mode      summaryMode
	saveInput textInput
	saveErr   error
	savedTo   string // last successful save path, shown in the status bar

	width  int
	height int
	scroll int
}

// summaryLoadedMsg carries the result of the summarize call.
type summaryLoadedMsg struct {
	summary string
	err     error
}

// summarySavedMsg carries the result of writing the summary to a local file.
type summarySavedMsg struct {
	path string
	err  error
}

func newSummaryModel(c *client, sessionID, sessionName string) summaryModel {
	return summaryModel{
		client:      c,
		sessionID:   sessionID,
		sessionName: sessionName,
		loading:     true,
		saveInput:   newTextInput(false),
	}
}

// loadSummary kicks off the summarize call. The moneypenny runs a one-shot
// agent over the full transcript, so this can be slow on large sessions.
func (m summaryModel) loadSummary() tea.Cmd {
	id := m.sessionID
	return func() tea.Msg {
		s, err := m.client.summarizeSession(id)
		return summaryLoadedMsg{summary: s, err: err}
	}
}

// defaultSavePath suggests a filename under the current working directory.
// We fall back to the home directory if CWD is unavailable. Hyphens replace
// spaces in the session name to keep the file path shell-friendly.
func defaultSavePath(sessionName string) string {
	base := strings.TrimSpace(sessionName)
	if base == "" {
		base = "session"
	}
	// Replace path-hostile characters with hyphens.
	base = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\t', '\n':
			return '-'
		case ' ':
			return '-'
		}
		return r
	}, base)
	if len(base) > 60 {
		base = base[:60]
	}
	name := base + "-summary.md"
	dir, err := os.Getwd()
	if err != nil || dir == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			dir = home
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, name)
}

func (m summaryModel) saveSummary(path string) tea.Cmd {
	content := m.summary
	return func() tea.Msg {
		if path == "" {
			return summarySavedMsg{err: fmt.Errorf("path is required")}
		}
		// Expand a leading ~/ or bare ~ to the user's home dir. Don't expand
		// ~user/ — that's a shell construct we don't try to mimic, and
		// matching it accidentally would prepend $HOME to a literal username.
		if path == "~" || strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, strings.TrimPrefix(path, "~"))
			}
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return summarySavedMsg{err: err}
		}
		return summarySavedMsg{path: path}
	}
}

// contentWidth returns the wrapping width used for the rendered summary.
// Kept in sync with the View() calculation so scroll clamping uses the
// same line count View() will produce.
func (m summaryModel) contentWidth() int {
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	return w
}

// viewHeight returns the number of visible content rows, matching View()'s
// reservation for the title and status bar.
func (m summaryModel) viewHeight() int {
	bottomHeight := 2
	if m.mode == summaryModeSavePath {
		bottomHeight = 4
		if m.saveErr != nil {
			bottomHeight = 5
		}
	}
	h := m.height - 4 - bottomHeight
	if h < 1 {
		h = 20
	}
	return h
}

// maxScroll returns the largest scroll offset that still keeps content
// visible. Returns 0 when the summary fits on screen.
func (m summaryModel) maxScroll() int {
	if m.summary == "" {
		return 0
	}
	total := len(strings.Split(wordWrap(m.summary, m.contentWidth()), "\n"))
	vh := m.viewHeight()
	if total <= vh {
		return 0
	}
	return total - vh
}

func (m summaryModel) Update(msg tea.Msg) (summaryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case summaryLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.summary = msg.summary
		return m, nil

	case summarySavedMsg:
		if msg.err != nil {
			m.saveErr = msg.err
			return m, nil
		}
		m.savedTo = msg.path
		m.mode = summaryModeView
		m.saveErr = nil
		m.saveInput.Reset()
		return m, nil

	case tea.KeyMsg:
		if m.mode == summaryModeSavePath {
			// Modal save-path input.
			switch msg.String() {
			case "esc":
				m.mode = summaryModeView
				m.saveErr = nil
				m.saveInput.Reset()
				return m, nil
			}
			handled, submitted := m.saveInput.HandleKey(msg)
			if submitted {
				path := strings.TrimSpace(m.saveInput.Value())
				return m, m.saveSummary(path)
			}
			if handled {
				return m, nil
			}
			return m, nil
		}

		max := m.maxScroll()
		switch msg.String() {
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			if m.scroll < max {
				m.scroll++
			}
		case "pgup":
			m.scroll -= 10
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "pgdown":
			m.scroll += 10
			if m.scroll > max {
				m.scroll = max
			}
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = max
		case "s":
			// Open the save-path modal pre-filled with a sensible default.
			if m.summary == "" {
				return m, nil
			}
			m.mode = summaryModeSavePath
			m.saveErr = nil
			m.saveInput.SetValue(defaultSavePath(m.sessionName))
			return m, nil
		}
	}
	return m, nil
}

func (m summaryModel) View() string {
	var b strings.Builder

	title := " Session Summary "
	if m.sessionName != "" {
		title = fmt.Sprintf(" Summary — %s ", m.sessionName)
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	viewHeight := m.viewHeight()
	contentWidth := m.contentWidth()

	switch {
	case m.loading:
		b.WriteString("  Summarizing session… (this may take a while on long histories)\n")
	case m.err != nil:
		b.WriteString("  " + lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	case m.summary == "":
		b.WriteString("  (no conversation history to summarize)\n")
	default:
		wrapped := wordWrap(m.summary, contentWidth)
		lines := strings.Split(wrapped, "\n")
		// Defensive clamp: Update() already keeps m.scroll within [0,
		// maxScroll], but a resize between updates could leave it stale.
		scroll := m.scroll
		if scroll > len(lines)-1 {
			scroll = len(lines) - 1
		}
		if scroll < 0 {
			scroll = 0
		}
		end := scroll + viewHeight
		if end > len(lines) {
			end = len(lines)
		}
		for _, line := range lines[scroll:end] {
			b.WriteString("  ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		// Pad to viewHeight so the status bar sticks to the bottom.
		for i := end - scroll; i < viewHeight; i++ {
			b.WriteString("\n")
		}
	}

	// Bottom area: save-path modal or status bar.
	if m.mode == summaryModeSavePath {
		b.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Render("  Save summary to:") + "\n")
		b.WriteString("  " + fieldActiveStyle.Render(m.saveInput.Render()) + "\n")
		if m.saveErr != nil {
			b.WriteString("  " + lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("Error: %v", m.saveErr)) + "\n")
		}
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Enter=save  Esc=cancel") + "\n")
		return b.String()
	}

	status := "↑/↓=scroll  g/G=top/bottom  s=save  Esc=back"
	if m.savedTo != "" {
		status = fmt.Sprintf("Saved to %s   ↑/↓=scroll  s=save again  Esc=back", m.savedTo)
	}
	b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  " + status))
	return b.String()
}
