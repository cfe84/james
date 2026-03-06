package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// chatModel displays conversation history and allows sending messages.
type chatModel struct {
	sessionID    string
	sessionName  string
	conversation []conversationTurn
	input        string
	width        int
	height       int
	scroll       int // scroll offset from bottom
	err          error
	loading      bool
	sending      bool
	client       *client
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

	case tea.KeyMsg:
		if m.sending {
			return m, nil // ignore input while sending
		}
		switch msg.String() {
		case "enter":
			prompt := strings.TrimSpace(m.input)
			if prompt != "" {
				m.input = ""
				m.sending = true
				m.err = nil
				// Add user message optimistically.
				m.conversation = append(m.conversation, conversationTurn{
					Role:    "user",
					Content: prompt,
				})
				m.scroll = 0
				return m, m.sendMessage(prompt)
			}
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case "ctrl+u":
			m.input = ""
		case "pgup":
			m.scroll += 10
		case "pgdown":
			m.scroll -= 10
			if m.scroll < 0 {
				m.scroll = 0
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			} else if msg.Type == tea.KeySpace {
				m.input += " "
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

	// Calculate available height for messages.
	msgHeight := m.height - 5 // title + input + status + borders

	// Render messages.
	var msgLines []string
	for _, turn := range m.conversation {
		var prefix string
		if turn.Role == "user" {
			prefix = userMsgStyle.Render("▶ you")
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
	prompt := inputPromptStyle.Render(" > ")
	cursor := "█"
	if m.sending {
		cursor = ""
	}
	inputLine := prompt + m.input + cursor
	b.WriteString(inputLine)

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
