package ui

import (
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// textInput is a reusable text input widget with cursor navigation,
// word movement (ctrl+b/f, alt+left/right), and optional multiline support.
// In multiline mode, ctrl+j / shift+enter inserts newlines and enter submits.
// In single-line mode, enter submits.
type textInput struct {
	text      string
	cursorPos int
	multiline bool
}

func newTextInput(multiline bool) textInput {
	return textInput{multiline: multiline}
}

// Value returns the current text.
func (t *textInput) Value() string { return t.text }

// SetValue sets the text and moves cursor to end.
func (t *textInput) SetValue(s string) {
	t.text = s
	t.cursorPos = len(s)
}

// Reset clears the text and cursor position.
func (t *textInput) Reset() {
	t.text = ""
	t.cursorPos = 0
}

// IsEmpty returns true if the text is empty.
func (t *textInput) IsEmpty() bool { return t.text == "" }

// HandleKey processes a key message. Returns:
//   - handled: true if the key was consumed by the input
//   - submitted: true if enter was pressed (submit action)
func (t *textInput) HandleKey(msg tea.KeyMsg) (handled, submitted bool) {
	switch msg.String() {
	case "enter":
		return true, true
	case "shift+enter", "alt+enter", "ctrl+j":
		if t.multiline {
			t.text = t.text[:t.cursorPos] + "\n" + t.text[t.cursorPos:]
			t.cursorPos++
			return true, false
		}
		return true, false
	case "backspace":
		if t.cursorPos > 0 {
			_, size := utf8.DecodeLastRuneInString(t.text[:t.cursorPos])
			t.text = t.text[:t.cursorPos-size] + t.text[t.cursorPos:]
			t.cursorPos -= size
		}
		return true, false
	case "delete":
		if t.cursorPos < len(t.text) {
			_, size := utf8.DecodeRuneInString(t.text[t.cursorPos:])
			t.text = t.text[:t.cursorPos] + t.text[t.cursorPos+size:]
		}
		return true, false
	case "left":
		if t.cursorPos > 0 {
			_, size := utf8.DecodeLastRuneInString(t.text[:t.cursorPos])
			t.cursorPos -= size
		}
		return true, false
	case "right":
		if t.cursorPos < len(t.text) {
			_, size := utf8.DecodeRuneInString(t.text[t.cursorPos:])
			t.cursorPos += size
		}
		return true, false
	case "alt+left", "ctrl+b":
		t.cursorPos = wordLeft(t.text, t.cursorPos)
		return true, false
	case "alt+right", "ctrl+f":
		t.cursorPos = wordRight(t.text, t.cursorPos)
		return true, false
	case "ctrl+backspace", "alt+backspace":
		// Delete previous word.
		newPos := wordLeft(t.text, t.cursorPos)
		t.text = t.text[:newPos] + t.text[t.cursorPos:]
		t.cursorPos = newPos
		return true, false
	case "ctrl+delete":
		// Delete next word.
		newPos := wordRight(t.text, t.cursorPos)
		t.text = t.text[:t.cursorPos] + t.text[newPos:]
		return true, false
	case "ctrl+r":
		t.text = ""
		t.cursorPos = 0
		return true, false
	case "home":
		t.cursorPos = 0
		return true, false
	case "end":
		t.cursorPos = len(t.text)
		return true, false
	default:
		if msg.Type == tea.KeyRunes {
			s := string(msg.Runes)
			t.text = t.text[:t.cursorPos] + s + t.text[t.cursorPos:]
			t.cursorPos += len(s)
			return true, false
		} else if msg.Type == tea.KeySpace {
			t.text = t.text[:t.cursorPos] + " " + t.text[t.cursorPos:]
			t.cursorPos++
			return true, false
		}
	}
	return false, false
}

// Render returns the text with a cursor block at the cursor position.
func (t *textInput) Render() string {
	return t.text[:t.cursorPos] + "█" + t.text[t.cursorPos:]
}
