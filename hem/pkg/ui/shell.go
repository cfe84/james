package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type shellCommandDoneMsg struct {
	output   string
	exitCode int
	err      error
}

// shellModel provides a remote shell on a moneypenny.
type shellModel struct {
	moneypenny string
	path       string
	title      string
	history    []shellEntry // command + output history
	input      string
	cursorPos  int
	width      int
	height     int
	scroll     int
	running    bool
	err        error
	client     *client
}

type shellEntry struct {
	command  string
	output   string
	exitCode int
}

func newShellModel(c *client, moneypenny, path, title string) shellModel {
	if title == "" {
		title = moneypenny
		if path != "" {
			title += ":" + path
		}
	}
	return shellModel{
		client:     c,
		moneypenny: moneypenny,
		path:       path,
		title:      title,
	}
}

func newShellModelFromSession(c *client, sessionID, sessionName string) shellModel {
	m := shellModel{
		client: c,
		title:  "Shell: " + sessionName,
	}
	if sessionName == "" {
		m.title = "Shell: " + truncate(sessionID, 20)
	}
	// Resolve moneypenny and path from session detail.
	detail, err := c.showSession(sessionID)
	if err != nil {
		m.err = err
	} else {
		m.moneypenny = detail.Moneypenny
		m.path = detail.Path
		m.title = "Shell: " + sessionName + " (" + m.moneypenny + ")"
		if sessionName == "" {
			m.title = "Shell: " + truncate(sessionID, 12) + " (" + m.moneypenny + ")"
		}
	}
	return m
}

func (m shellModel) runCommand(command string) tea.Cmd {
	mp := m.moneypenny
	path := m.path
	return func() tea.Msg {
		result, err := m.client.runCommand(mp, path, command)
		if err != nil {
			return shellCommandDoneMsg{err: err}
		}
		return shellCommandDoneMsg{output: result.Output, exitCode: result.ExitCode}
	}
}

func (m shellModel) Init() tea.Cmd {
	return nil
}

func (m shellModel) Update(msg tea.Msg) (shellModel, tea.Cmd) {
	switch msg := msg.(type) {
	case shellCommandDoneMsg:
		m.running = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			if len(m.history) > 0 {
				last := &m.history[len(m.history)-1]
				last.output = msg.output
				last.exitCode = msg.exitCode
			}
			m.scroll = 0
		}

	case tea.KeyMsg:
		if m.running {
			return m, nil
		}

		switch msg.String() {
		case "enter":
			command := strings.TrimSpace(m.input)
			if command != "" {
				m.input = ""
				m.cursorPos = 0
				m.running = true
				m.err = nil
				m.history = append(m.history, shellEntry{command: command})
				m.scroll = 0
				return m, m.runCommand(command)
			}
		case "backspace":
			if m.cursorPos > 0 {
				_, size := utf8.DecodeLastRuneInString(m.input[:m.cursorPos])
				m.input = m.input[:m.cursorPos-size] + m.input[m.cursorPos:]
				m.cursorPos -= size
			}
		case "left":
			if m.cursorPos > 0 {
				_, size := utf8.DecodeLastRuneInString(m.input[:m.cursorPos])
				m.cursorPos -= size
			}
		case "right":
			if m.cursorPos < len(m.input) {
				_, size := utf8.DecodeRuneInString(m.input[m.cursorPos:])
				m.cursorPos += size
			}
		case "alt+left", "ctrl+b":
			m.cursorPos = wordLeft(m.input, m.cursorPos)
		case "alt+right", "ctrl+f":
			m.cursorPos = wordRight(m.input, m.cursorPos)
		case "home", "ctrl+a":
			m.cursorPos = 0
		case "end", "ctrl+e":
			m.cursorPos = len(m.input)
		case "ctrl+u":
			m.input = ""
			m.cursorPos = 0
		case "pgup":
			m.scroll += 10
		case "pgdown", "ctrl+d":
			m.scroll -= 10
			if m.scroll < 0 {
				m.scroll = 0
			}
		default:
			if msg.Type == tea.KeyRunes {
				s := string(msg.Runes)
				m.input = m.input[:m.cursorPos] + s + m.input[m.cursorPos:]
				m.cursorPos += len(s)
			} else if msg.Type == tea.KeySpace {
				m.input = m.input[:m.cursorPos] + " " + m.input[m.cursorPos:]
				m.cursorPos++
			}
		}
	}
	return m, nil
}

func (m shellModel) View() string {
	var b strings.Builder

	// Title bar
	b.WriteString(titleStyle.Render(" " + m.title + " "))
	b.WriteString("\n")

	if m.err != nil && len(m.history) == 0 {
		b.WriteString(fmt.Sprintf("\n  Error: %v\n", m.err))
		return b.String()
	}

	// Calculate input area height.
	inputWidth := m.width - 4
	if inputWidth < 20 {
		inputWidth = 80
	}
	inputLineCount := 1

	// Available height for output.
	outputHeight := m.height - 3 - inputLineCount // title + error + input
	if outputHeight < 1 {
		outputHeight = 20
	}

	// Render history lines.
	var outputLines []string
	cmdStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	exitCodeStyle := lipgloss.NewStyle().Foreground(colorDanger)

	for _, entry := range m.history {
		outputLines = append(outputLines, cmdStyle.Render("$ "+entry.command))
		if entry.output != "" {
			for _, line := range strings.Split(strings.TrimRight(entry.output, "\n"), "\n") {
				outputLines = append(outputLines, "  "+line)
			}
		}
		if entry.exitCode != 0 {
			outputLines = append(outputLines, exitCodeStyle.Render(fmt.Sprintf("  exit code: %d", entry.exitCode)))
		}
		outputLines = append(outputLines, "")
	}

	if m.running {
		outputLines = append(outputLines, lipgloss.NewStyle().Foreground(colorWarning).Render("  running..."))
		outputLines = append(outputLines, "")
	}

	// Apply scroll.
	totalLines := len(outputLines)
	end := totalLines - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - outputHeight
	if start < 0 {
		start = 0
	}
	if end > totalLines {
		end = totalLines
	}

	for i := start; i < end; i++ {
		b.WriteString(outputLines[i])
		b.WriteString("\n")
	}

	// Pad remaining space.
	rendered := end - start
	for i := rendered; i < outputHeight; i++ {
		b.WriteString("\n")
	}

	// Error display.
	if m.err != nil {
		errLine := lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.err))
		b.WriteString(errLine)
		b.WriteString("\n")
	}

	// Input line.
	prompt := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" $ ")
	cursor := "█"
	if m.running {
		cursor = ""
	}
	displayInput := m.input[:m.cursorPos] + cursor + m.input[m.cursorPos:]
	b.WriteString(prompt + displayInput)

	return b.String()
}
