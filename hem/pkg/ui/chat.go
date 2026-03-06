package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type chatSessionStoppedMsg struct{ err error }
type chatSessionCompletedMsg struct{ err error }
type chatSessionDeletedMsg struct{ err error }

// chatModel displays conversation history and allows sending messages.
type chatModel struct {
	sessionID    string
	sessionName  string
	conversation []conversationTurn
	input        string
	cursorPos    int
	width        int
	height       int
	scroll       int // scroll offset from bottom
	err          error
	loading      bool
	sending       bool
	commandMode   bool
	confirmDelete bool
	client        *client
}

func newChatModel(c *client, sessionID, sessionName string) chatModel {
	return chatModel{
		client:      c,
		sessionID:   sessionID,
		sessionName: sessionName,
		loading:     true,
	}
}

// Messages
type historyLoadedMsg struct {
	conversation []conversationTurn
	err          error
}

type messageSentMsg struct {
	response string
	err      error
}

func (m chatModel) loadHistory() tea.Cmd {
	return func() tea.Msg {
		turns, err := m.client.getHistory(m.sessionID)
		return historyLoadedMsg{conversation: turns, err: err}
	}
}

func (m chatModel) sendMessage(prompt string) tea.Cmd {
	return func() tea.Msg {
		resp, err := m.client.continueSession(m.sessionID, prompt)
		return messageSentMsg{response: resp, err: err}
	}
}

func (m chatModel) Init() tea.Cmd {
	return m.loadHistory()
}

func (m chatModel) Update(msg tea.Msg) (chatModel, tea.Cmd) {
	switch msg := msg.(type) {
	case historyLoadedMsg:
		m.loading = false
		m.conversation = msg.conversation
		m.err = msg.err
		m.scroll = 0

	case messageSentMsg:
		m.sending = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			// Reload history to get the full updated conversation.
			return m, m.loadHistory()
		}

	case chatSessionStoppedMsg:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.commandMode = false
		}

	case tea.KeyMsg:
		if m.sending {
			return m, nil // ignore input while sending
		}

		if m.commandMode {
			// Most command mode keys are handled by updateChat in ui.go.
			// Chat model only handles enter (resume) and scroll.
			switch msg.String() {
			case "enter":
				m.commandMode = false
				m.confirmDelete = false
			case "pgup":
				m.scroll += 10
			case "pgdown":
				m.scroll -= 10
				if m.scroll < 0 {
					m.scroll = 0
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "enter":
			prompt := strings.TrimSpace(m.input)
			if prompt != "" {
				m.input = ""
				m.cursorPos = 0
				m.sending = true
				m.err = nil
				m.conversation = append(m.conversation, conversationTurn{
					Role:    "user",
					Content: prompt,
				})
				m.scroll = 0
				return m, m.sendMessage(prompt)
			}
		case "shift+enter":
			m.input = m.input[:m.cursorPos] + "\n" + m.input[m.cursorPos:]
			m.cursorPos++
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
		case "home":
			m.cursorPos = 0
		case "end":
			m.cursorPos = len(m.input)
		case "ctrl+u":
			m.input = ""
			m.cursorPos = 0
		case "pgup":
			m.scroll += 10
		case "pgdown":
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

func (m chatModel) View() string {
	var b strings.Builder

	// Title bar
	title := fmt.Sprintf(" Chat: %s ", m.sessionName)
	if m.sessionName == "" {
		title = fmt.Sprintf(" Chat: %s ", truncate(m.sessionID, 20))
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")

	if m.loading {
		b.WriteString("\n  Loading conversation...\n")
		return b.String()
	}

	// Calculate input area height for layout.
	inputWidth := m.width - 4
	if inputWidth < 20 {
		inputWidth = 80
	}
	inputLineCount := 1
	if !m.commandMode {
		cursor := "█"
		if m.sending {
			cursor = ""
		}
		displayInput := m.input[:m.cursorPos] + cursor + m.input[m.cursorPos:]
		wrapped := wordWrap(displayInput, inputWidth)
		inputLineCount = strings.Count(wrapped, "\n") + 1
	}

	// Calculate available height for messages.
	msgHeight := m.height - 3 - inputLineCount // title + error + input

	// Render messages.
	var msgLines []string
	for _, turn := range m.conversation {
		var prefix string
		if turn.Role == "user" {
			prefix = userMsgStyle.Render("🧑‍💻 you")
		} else {
			prefix = assistantMsgStyle.Render("🤖 assistant")
		}
		if turn.CreatedAt != "" {
			prefix += " " + lipgloss.NewStyle().Foreground(colorMuted).Render(turn.CreatedAt)
		}
		msgLines = append(msgLines, prefix)

		// Render content.
		contentWidth := m.width - 4
		if contentWidth < 20 {
			contentWidth = 80
		}
		var rendered string
		if turn.Role == "assistant" {
			rendered = renderMarkdown(turn.Content, contentWidth)
		} else {
			rendered = wordWrap(turn.Content, contentWidth)
		}
		for _, line := range strings.Split(rendered, "\n") {
			if turn.Role == "assistant" {
				msgLines = append(msgLines, "  "+line)
			} else {
				msgLines = append(msgLines, "  "+msgContentStyle.Render(line))
			}
		}
		msgLines = append(msgLines, "")
	}

	if m.sending {
		msgLines = append(msgLines, assistantMsgStyle.Render("🤖 thinking..."))
		msgLines = append(msgLines, "")
	}

	// Apply scroll and show only what fits.
	if msgHeight < 1 {
		msgHeight = 20
	}
	totalLines := len(msgLines)
	end := totalLines - m.scroll
	if end < 0 {
		end = 0
	}
	start := end - msgHeight
	if start < 0 {
		start = 0
	}
	if end > totalLines {
		end = totalLines
	}

	for i := start; i < end; i++ {
		b.WriteString(msgLines[i])
		b.WriteString("\n")
	}

	// Pad remaining space.
	rendered := end - start
	for i := rendered; i < msgHeight; i++ {
		b.WriteString("\n")
	}

	// Error display
	if m.err != nil {
		errLine := lipgloss.NewStyle().Foreground(colorDanger).Render(fmt.Sprintf("  Error: %v", m.err))
		b.WriteString(errLine)
		b.WriteString("\n")
	}

	// Input line
	if m.commandMode {
		cmdBar := lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render(" COMMAND MODE ")
		if m.confirmDelete {
			cmdBar += lipgloss.NewStyle().Foreground(colorDanger).Bold(true).Render(
				"  Press d again to confirm delete, any other key to cancel")
		}
		b.WriteString(cmdBar)
	} else {
		prompt := inputPromptStyle.Render(" > ")
		cursor := "█"
		if m.sending {
			cursor = ""
		}
		displayInput := m.input[:m.cursorPos] + cursor + m.input[m.cursorPos:]
		wrapped := wordWrap(displayInput, inputWidth)
		lines := strings.Split(wrapped, "\n")
		for i, line := range lines {
			if i == 0 {
				b.WriteString(prompt + line)
			} else {
				b.WriteString("\n   " + line)
			}
		}
	}

	return b.String()
}

// renderMarkdown renders markdown content using glamour.
func renderMarkdown(content string, width int) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return wordWrap(content, width)
	}
	out, err := r.Render(content)
	if err != nil {
		return wordWrap(content, width)
	}
	// Trim trailing whitespace from glamour output.
	return strings.TrimRight(out, "\n ")
}

// wordWrap wraps text at the given width, breaking on spaces.
func wordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= width {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}
		words := strings.Fields(line)
		currentLine := ""
		for _, word := range words {
			if currentLine == "" {
				currentLine = word
			} else if lipgloss.Width(currentLine+" "+word) <= width {
				currentLine += " " + word
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine)
				currentLine = word
			}
		}
		if currentLine != "" {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine)
		}
	}
	return result.String()
}
