package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// traitsModel displays and manages the trait list.
type traitsModel struct {
	traits        []traitInfo
	cursor        int
	width         int
	height        int
	err           error
	loading       bool
	client        *client
	confirmDelete bool // first-press flag for two-step delete confirmation
}

type traitsLoadedMsg struct {
	traits []traitInfo
	err    error
}

type traitDeletedMsg struct{ err error }

func newTraitsModel(c *client) traitsModel {
	return traitsModel{
		client:  c,
		loading: true,
	}
}

func (m traitsModel) loadTraits() tea.Cmd {
	return func() tea.Msg {
		traits, err := m.client.listTraits()
		return traitsLoadedMsg{traits: traits, err: err}
	}
}

func (m traitsModel) deleteTrait(nameOrID string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteTrait(nameOrID)
		return traitDeletedMsg{err: err}
	}
}

func (m traitsModel) Init() tea.Cmd {
	return m.loadTraits()
}

func (m traitsModel) Update(msg tea.Msg) (traitsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case traitsLoadedMsg:
		m.loading = false
		m.traits = msg.traits
		m.err = msg.err
		if m.cursor >= len(m.traits) {
			m.cursor = max(0, len(m.traits)-1)
		}

	case traitDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, m.loadTraits()

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.traits)-1 {
				m.cursor++
			}
		case "r":
			m.loading = true
			return m, m.loadTraits()
		}
	}
	return m, nil
}

func (m traitsModel) selectedTrait() *traitInfo {
	if len(m.traits) == 0 || m.cursor >= len(m.traits) {
		return nil
	}
	return &m.traits[m.cursor]
}

func (m traitsModel) View() string {
	if m.loading {
		return "\n  Loading traits..."
	}
	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v", m.err)
	}
	if len(m.traits) == 0 {
		return "\n  No traits. Create one with: hem create trait --name NAME --prompt TEXT"
	}

	var b strings.Builder

	header := fmt.Sprintf("  %-24s %-50s", "Name", "Prompt")
	b.WriteString(sessionHeaderStyle.Render(header))
	b.WriteString("\n")

	maxRows := m.height - 4
	if maxRows < 1 {
		maxRows = 10
	}

	start := 0
	if m.cursor >= maxRows {
		start = m.cursor - maxRows + 1
	}
	end := start + maxRows
	if end > len(m.traits) {
		end = len(m.traits)
	}

	for i := start; i < end; i++ {
		t := m.traits[i]
		name := truncate(t.Name, 22)
		preview := truncate(t.Preview, 48)
		line := fmt.Sprintf("  %-24s %-50s", name, preview)
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

	if len(m.traits) > maxRows {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.traits))))
		b.WriteString("\n")
	}

	if m.confirmDelete {
		t := m.selectedTrait()
		name := ""
		if t != nil {
			name = t.Name
		}
		warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
		hintStyle := lipgloss.NewStyle().Foreground(colorMuted)
		b.WriteString("\n  " + warnStyle.Render(fmt.Sprintf("Delete trait %q?", name)) +
			"  " + hintStyle.Render("press d again to confirm · any other key cancels"))
		b.WriteString("\n")
	}

	return b.String()
}

type traitCreatedMsg struct{ err error }
type traitUpdatedMsg struct{ err error }

// editTraitModel is a form for creating or editing a trait. When traitID is
// empty the form creates a new trait; otherwise it updates the existing one.
type editTraitModel struct {
	traitID  string
	fields   []formField
	original []string
	cursor   int
	width    int
	height   int
	err      error
	saving   bool
	client   *client
}

func newCreateTraitModel(c *client) editTraitModel {
	spInput := newTextInput(true)
	fields := []formField{
		{label: "Name", flag: "--name", value: ""},
		{label: "Prompt", flag: "--prompt", value: "", input: &spInput},
	}
	original := make([]string, len(fields))
	for i, f := range fields {
		original[i] = f.value
	}
	return editTraitModel{
		client:   c,
		fields:   fields,
		original: original,
	}
}

func newEditTraitModel(c *client, t *traitDetail) editTraitModel {
	spInput := newTextInput(true)
	spInput.SetValue(t.Prompt)
	fields := []formField{
		{label: "Name", flag: "--name", value: t.Name, cursorPos: len(t.Name)},
		{label: "Prompt", flag: "--prompt", value: t.Prompt, cursorPos: len(t.Prompt), input: &spInput},
	}
	original := make([]string, len(fields))
	for i, f := range fields {
		original[i] = f.value
	}
	return editTraitModel{
		client:   c,
		traitID:  t.ID,
		fields:   fields,
		original: original,
	}
}

func (m editTraitModel) save() tea.Cmd {
	isCreate := m.traitID == ""
	name := m.fields[0].value
	prompt := m.fields[1].value
	id := m.traitID
	client := m.client
	return func() tea.Msg {
		if isCreate {
			err := client.createTrait(name, prompt)
			return traitCreatedMsg{err: err}
		}
		fields := map[string]string{}
		if name != m.original[0] {
			fields["--name"] = name
		}
		if prompt != m.original[1] {
			fields["--prompt"] = prompt
		}
		if len(fields) == 0 {
			return traitUpdatedMsg{err: nil}
		}
		err := client.updateTrait(id, fields)
		return traitUpdatedMsg{err: err}
	}
}

func (m editTraitModel) Update(msg tea.Msg) (editTraitModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.saving {
			return m, nil
		}
		field := &m.fields[m.cursor]

		switch msg.String() {
		case "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down":
			if m.cursor < len(m.fields)-1 {
				m.cursor++
			}
			return m, nil
		case "tab":
			m.cursor = (m.cursor + 1) % len(m.fields)
			return m, nil
		}

		// Delegate to textInput if the field has one (the Prompt field).
		if field.input != nil {
			switch msg.String() {
			case "enter":
				m.saving = true
				field.syncFromInput()
				return m, m.save()
			case "ctrl+u":
				field.input.Reset()
				field.syncFromInput()
				return m, nil
			default:
				handled, submitted := field.input.HandleKey(msg)
				if submitted {
					m.saving = true
					field.syncFromInput()
					return m, m.save()
				}
				if handled {
					field.syncFromInput()
				}
			}
			return m, nil
		}

		switch msg.String() {
		case "enter":
			m.saving = true
			return m, m.save()
		case "backspace":
			if field.cursorPos > 0 && len(field.value) > 0 {
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
	}
	return m, nil
}

func (m editTraitModel) View() string {
	var b strings.Builder

	title := " New Trait "
	if m.traitID != "" {
		title = fmt.Sprintf(" Edit Trait: %s ", truncate(m.fields[0].value, 20))
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	const valueIndent = 27
	maxValueWidth := m.width - valueIndent - 2
	if maxValueWidth < 20 {
		maxValueWidth = 20
	}

	for i, f := range m.fields {
		label := labelStyle.Render(f.label + ":")

		changed := ""
		if f.value != m.original[i] {
			changed = lipgloss.NewStyle().Foreground(colorWarning).Render(" *")
		}

		var value string
		if i == m.cursor {
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
		} else {
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
		b.WriteString("  " + label + " " + value + changed + "\n")
	}

	b.WriteString("\n")
	if m.saving {
		b.WriteString("  Saving...")
	} else {
		b.WriteString(statusDescStyle.Render(" Enter ") + " save  " +
			statusDescStyle.Render(" Tab ") + " next field  " +
			statusDescStyle.Render(" Esc ") + " cancel  " +
			statusDescStyle.Render(" Ctrl+U ") + " clear  " +
			statusDescStyle.Render(" Ctrl+J ") + " newline")
	}

	if m.err != nil {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorDanger).Render(m.err.Error()))
	}

	return dialogStyle.Render(b.String())
}
