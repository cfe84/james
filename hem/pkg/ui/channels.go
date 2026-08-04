package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// channelStage is the current step of the channels view state machine.
type channelStage int

const (
	chStageList channelStage = iota
	chStageProvider
	chStageMethod
	chStageSearchInput
	chStageSearchResults
	chStageIDInput
	chStageOptions
)

// channelsModel displays and manages the external communication channels bound
// to a session (Teams chats today). It embeds a small create-flow state machine:
// pick provider -> choose method (search vs id) -> search/pick or type id -> bind.
type channelsModel struct {
	client      *client
	sessionID   string
	sessionName string
	width       int
	height      int
	loading     bool
	err         error
	status      string

	stage         channelStage
	channels      []channelInfo
	cursor        int
	confirmDelete bool

	providers        []channelProviderInfo
	provCursor       int
	selectedProvider channelProviderInfo

	searchInput  string
	results      []channelTargetInfo
	resultCursor int

	idInput    string
	labelInput string
	idField    int // 0 = target id, 1 = label

	// Options stage (mention gate + sender policy), shared by create and edit.
	// editingID is 0 when creating a new binding, else the id being edited.
	editingID      int64
	pendingTargetID string
	pendingLabel    string
	mentionInput    string
	optAllowAnyone  bool
	optField        int // 0 = mention, 1 = allow-anyone
}

type channelsLoadedMsg struct {
	channels []channelInfo
	err      error
}

type channelProvidersLoadedMsg struct {
	providers []channelProviderInfo
	err       error
}

type channelSearchMsg struct {
	results []channelTargetInfo
	err     error
}

type channelActionMsg struct {
	status string
	err    error
}

func newChannelsModel(c *client, sessionID, sessionName string) channelsModel {
	return channelsModel{
		client:      c,
		sessionID:   sessionID,
		sessionName: sessionName,
		loading:     true,
		stage:       chStageList,
	}
}

func (m channelsModel) loadChannels() tea.Cmd {
	return func() tea.Msg {
		channels, err := m.client.listChannels(m.sessionID)
		return channelsLoadedMsg{channels: channels, err: err}
	}
}

func (m channelsModel) loadProviders() tea.Cmd {
	return func() tea.Msg {
		providers, err := m.client.listChannelProviders(m.sessionID)
		return channelProvidersLoadedMsg{providers: providers, err: err}
	}
}

func (m channelsModel) doSearch(provider, query string) tea.Cmd {
	return func() tea.Msg {
		results, err := m.client.searchChannelTargets(m.sessionID, provider, query)
		return channelSearchMsg{results: results, err: err}
	}
}

func (m channelsModel) createChannel(provider, targetID, label, mention string, allowAnyone bool) tea.Cmd {
	return func() tea.Msg {
		err := m.client.createChannel(m.sessionID, provider, targetID, label, mention, allowAnyone)
		if err != nil {
			return channelActionMsg{err: err}
		}
		return channelActionMsg{status: "Channel bound"}
	}
}

func (m channelsModel) updateChannel(id int64, mention string, allowAnyone bool) tea.Cmd {
	return func() tea.Msg {
		err := m.client.updateChannel(m.sessionID, id, mention, true, allowAnyone, true)
		if err != nil {
			return channelActionMsg{err: err}
		}
		return channelActionMsg{status: "Channel updated"}
	}
}

func (m channelsModel) deleteChannel(id int64) tea.Cmd {
	return func() tea.Msg {
		err := m.client.deleteChannel(m.sessionID, id)
		if err != nil {
			return channelActionMsg{err: err}
		}
		return channelActionMsg{status: "Channel deleted"}
	}
}

func (m channelsModel) toggleChannel(id int64, enabled bool) tea.Cmd {
	return func() tea.Msg {
		err := m.client.setChannelEnabled(m.sessionID, id, enabled)
		if err != nil {
			return channelActionMsg{err: err}
		}
		state := "enabled"
		if !enabled {
			state = "disabled"
		}
		return channelActionMsg{status: "Channel " + state}
	}
}

func (m channelsModel) selectedChannel() *channelInfo {
	if len(m.channels) == 0 || m.cursor >= len(m.channels) {
		return nil
	}
	return &m.channels[m.cursor]
}

// backToList resets the create flow and returns to the channel list. It is used
// by the parent Esc handler so Esc in a sub-stage does not leave the view.
func (m channelsModel) backToList() channelsModel {
	m.stage = chStageList
	m.err = nil
	m.searchInput = ""
	m.results = nil
	m.resultCursor = 0
	m.idInput = ""
	m.labelInput = ""
	m.idField = 0
	m.provCursor = 0
	m.editingID = 0
	m.pendingTargetID = ""
	m.pendingLabel = ""
	m.mentionInput = ""
	m.optAllowAnyone = false
	m.optField = 0
	return m
}

func (m channelsModel) Init() tea.Cmd {
	return m.loadChannels()
}

func (m channelsModel) Update(msg tea.Msg) (channelsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case channelsLoadedMsg:
		m.loading = false
		m.channels = msg.channels
		m.err = msg.err
		if m.cursor >= len(m.channels) {
			m.cursor = max(0, len(m.channels)-1)
		}

	case channelProvidersLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.providers = msg.providers
		if msg.err == nil {
			// Auto-select when a single provider is available.
			if len(m.providers) == 1 {
				m.selectedProvider = m.providers[0]
				m = m.advanceFromProvider()
			} else {
				m.stage = chStageProvider
				m.provCursor = 0
			}
		}

	case channelSearchMsg:
		m.loading = false
		m.err = msg.err
		m.results = msg.results
		m.resultCursor = 0
		if msg.err == nil {
			m.stage = chStageSearchResults
		}

	case channelActionMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.status = msg.status
		m = m.backToList()
		m.loading = true
		return m, m.loadChannels()

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m channelsModel) handleKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch m.stage {
	case chStageList:
		return m.handleListKey(msg)
	case chStageProvider:
		return m.handleProviderKey(msg)
	case chStageMethod:
		return m.handleMethodKey(msg)
	case chStageSearchInput:
		return m.handleSearchInputKey(msg)
	case chStageSearchResults:
		return m.handleResultsKey(msg)
	case chStageIDInput:
		return m.handleIDInputKey(msg)
	case chStageOptions:
		return m.handleOptionsKey(msg)
	}
	return m, nil
}

func (m channelsModel) handleListKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	wasConfirming := m.confirmDelete
	m.confirmDelete = false
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.channels)-1 {
			m.cursor++
		}
	case "n":
		m.err = nil
		m.status = ""
		m.loading = true
		return m, m.loadProviders()
	case "e":
		c := m.selectedChannel()
		if c != nil {
			return m, m.toggleChannel(c.ID, !c.Enabled)
		}
	case "d":
		c := m.selectedChannel()
		if c != nil {
			if !wasConfirming {
				m.confirmDelete = true
				return m, nil
			}
			return m, m.deleteChannel(c.ID)
		}
	case "r":
		m.loading = true
		return m, m.loadChannels()
	case "m":
		c := m.selectedChannel()
		if c != nil {
			m.editingID = c.ID
			m.mentionInput = c.Mention
			m.optAllowAnyone = c.AllowAnyone
			m.optField = 0
			m.err = nil
			m.status = ""
			m.stage = chStageOptions
		}
	case "a":
		c := m.selectedChannel()
		if c != nil {
			return m, m.updateChannel(c.ID, c.Mention, !c.AllowAnyone)
		}
	}
	return m, nil
}

func (m channelsModel) handleProviderKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.provCursor > 0 {
			m.provCursor--
		}
	case "down", "j":
		if m.provCursor < len(m.providers)-1 {
			m.provCursor++
		}
	case "enter":
		if m.provCursor < len(m.providers) {
			m.selectedProvider = m.providers[m.provCursor]
			m = m.advanceFromProvider()
		}
	}
	return m, nil
}

// advanceFromProvider chooses the next stage based on the selected provider's
// capabilities: both -> method picker; search-only -> search; id-only -> id.
func (m channelsModel) advanceFromProvider() channelsModel {
	p := m.selectedProvider
	switch {
	case p.Search && p.ByID:
		m.stage = chStageMethod
	case p.Search:
		m.stage = chStageSearchInput
	case p.ByID:
		m.stage = chStageIDInput
	default:
		// No documented capability; fall back to manual id entry.
		m.stage = chStageIDInput
	}
	return m
}

func (m channelsModel) handleMethodKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "s", "1":
		m.stage = chStageSearchInput
	case "i", "2":
		m.stage = chStageIDInput
	}
	return m, nil
}

func (m channelsModel) handleSearchInputKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		q := strings.TrimSpace(m.searchInput)
		if q == "" {
			return m, nil
		}
		m.loading = true
		m.err = nil
		return m, m.doSearch(m.selectedProvider.Name, q)
	case "backspace":
		if len(m.searchInput) > 0 {
			m.searchInput = trimLastRune(m.searchInput)
		}
	case " ":
		m.searchInput += " "
	default:
		if msg.Type == tea.KeyRunes {
			m.searchInput += string(msg.Runes)
		}
	}
	return m, nil
}

func (m channelsModel) handleResultsKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.resultCursor > 0 {
			m.resultCursor--
		}
	case "down", "j":
		if m.resultCursor < len(m.results)-1 {
			m.resultCursor++
		}
	case "enter":
		if m.resultCursor < len(m.results) {
			t := m.results[m.resultCursor]
			label := t.Label
			if label == "" {
				label = t.Detail
			}
			m.pendingTargetID = t.ID
			m.pendingLabel = label
			m.mentionInput = ""
			m.optAllowAnyone = false
			m.optField = 0
			m.editingID = 0
			m.stage = chStageOptions
		}
	}
	return m, nil
}

func (m channelsModel) handleIDInputKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "tab":
		m.idField = (m.idField + 1) % 2
	case "enter":
		id := strings.TrimSpace(m.idInput)
		if id == "" {
			return m, nil
		}
		m.pendingTargetID = id
		m.pendingLabel = strings.TrimSpace(m.labelInput)
		m.mentionInput = ""
		m.optAllowAnyone = false
		m.optField = 0
		m.editingID = 0
		m.stage = chStageOptions
		return m, nil
	case "backspace":
		if m.idField == 0 {
			m.idInput = trimLastRune(m.idInput)
		} else {
			m.labelInput = trimLastRune(m.labelInput)
		}
	case " ":
		if m.idField == 0 {
			m.idInput += " "
		} else {
			m.labelInput += " "
		}
	default:
		if msg.Type == tea.KeyRunes {
			if m.idField == 0 {
				m.idInput += string(msg.Runes)
			} else {
				m.labelInput += string(msg.Runes)
			}
		}
	}
	return m, nil
}

func (m channelsModel) handleOptionsKey(msg tea.KeyMsg) (channelsModel, tea.Cmd) {
	switch msg.String() {
	case "tab", "down", "up":
		m.optField = (m.optField + 1) % 2
	case " ":
		if m.optField == 1 {
			m.optAllowAnyone = !m.optAllowAnyone
		} else {
			m.mentionInput += " "
		}
	case "left", "right":
		if m.optField == 1 {
			m.optAllowAnyone = !m.optAllowAnyone
		}
	case "backspace":
		if m.optField == 0 {
			m.mentionInput = trimLastRune(m.mentionInput)
		}
	case "enter":
		mention := strings.TrimSpace(m.mentionInput)
		if m.editingID != 0 {
			return m, m.updateChannel(m.editingID, mention, m.optAllowAnyone)
		}
		return m, m.createChannel(m.selectedProvider.Name, m.pendingTargetID, m.pendingLabel, mention, m.optAllowAnyone)
	default:
		if m.optField == 0 && msg.Type == tea.KeyRunes {
			m.mentionInput += string(msg.Runes)
		}
	}
	return m, nil
}

func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func (m channelsModel) View() string {
	title := "  Channels"
	if m.sessionName != "" {
		title = "  Channels · " + m.sessionName
	}
	var b strings.Builder
	b.WriteString(sessionHeaderStyle.Render(title))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString("  Loading…")
		return b.String()
	}

	switch m.stage {
	case chStageList:
		m.renderList(&b)
	case chStageProvider:
		m.renderProviders(&b)
	case chStageMethod:
		m.renderMethod(&b)
	case chStageSearchInput:
		m.renderSearchInput(&b)
	case chStageSearchResults:
		m.renderResults(&b)
	case chStageIDInput:
		m.renderIDInput(&b)
	case chStageOptions:
		m.renderOptions(&b)
	}

	if m.err != nil {
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
		b.WriteString("\n  " + errStyle.Render("Error: "+m.err.Error()))
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render(
			"(the channel provider may require authentication — run `agency` to sign in)"))
	} else if m.status != "" {
		b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render(m.status))
	}
	return b.String()
}

func (m channelsModel) renderList(b *strings.Builder) {
	if len(m.channels) == 0 {
		b.WriteString("  No channels bound. Press " +
			lipgloss.NewStyle().Bold(true).Render("n") + " to add one.")
		return
	}
	header := fmt.Sprintf("  %-9s %-22s %-8s %-12s %-8s %s", "Provider", "Label", "Enabled", "Mention", "Senders", "Error")
	b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(header))
	b.WriteString("\n")
	for i, c := range m.channels {
		enabled := "no"
		if c.Enabled {
			enabled = "yes"
		}
		mention := "—"
		if c.Mention != "" {
			mention = "@" + c.Mention
		}
		senders := "owner"
		if c.AllowAnyone {
			senders = "anyone"
		}
		line := fmt.Sprintf("  %-9s %-22s %-8s %-12s %-8s %s",
			truncate(c.Provider, 8), truncate(c.Label, 21), enabled,
			truncate(mention, 11), senders, truncate(c.LastErr, 24))
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
	if m.confirmDelete {
		c := m.selectedChannel()
		label := ""
		if c != nil {
			label = c.Label
		}
		warn := lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
		hint := lipgloss.NewStyle().Foreground(colorMuted)
		b.WriteString("\n  " + warn.Render(fmt.Sprintf("Delete channel %q?", label)) +
			"  " + hint.Render("press d again to confirm · any other key cancels"))
		b.WriteString("\n")
	}
}

func (m channelsModel) renderProviders(b *strings.Builder) {
	b.WriteString("  Select a channel provider:\n\n")
	for i, p := range m.providers {
		caps := []string{}
		if p.Search {
			caps = append(caps, "search")
		}
		if p.ByID {
			caps = append(caps, "by-id")
		}
		line := fmt.Sprintf("  %-12s (%s)", p.Name, strings.Join(caps, ", "))
		if i == m.provCursor {
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("↑/↓ move · ↵ select · esc back"))
}

func (m channelsModel) renderMethod(b *strings.Builder) {
	b.WriteString(fmt.Sprintf("  Add a %s channel — choose a method:\n\n", m.selectedProvider.Name))
	b.WriteString("  " + lipgloss.NewStyle().Bold(true).Render("s") + "  Search for a chat\n")
	b.WriteString("  " + lipgloss.NewStyle().Bold(true).Render("i") + "  Enter a channel id directly\n")
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("esc back"))
}

func (m channelsModel) renderSearchInput(b *strings.Builder) {
	b.WriteString(fmt.Sprintf("  Search %s chats:\n\n", m.selectedProvider.Name))
	b.WriteString("  Query: " + inputBoxStyle(m.searchInput))
	b.WriteString("\n\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("↵ search · esc back"))
}

func (m channelsModel) renderResults(b *strings.Builder) {
	if len(m.results) == 0 {
		b.WriteString("  No matching chats found.")
		b.WriteString("\n\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("esc back"))
		return
	}
	b.WriteString("  Select a chat to bind:\n\n")
	for i, t := range m.results {
		label := t.Label
		if label == "" {
			label = t.ID
		}
		line := fmt.Sprintf("  %-32s %s", truncate(label, 31), truncate(t.Detail, 40))
		if i == m.resultCursor {
			b.WriteString(sessionSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionNormalStyle.Render(line))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("↑/↓ move · ↵ bind · esc back"))
}

func (m channelsModel) renderIDInput(b *strings.Builder) {
	b.WriteString(fmt.Sprintf("  Add a %s channel by id:\n\n", m.selectedProvider.Name))
	idLabel := "  Target id: "
	labelLabel := "  Label:     "
	if m.idField == 0 {
		idLabel = "> Target id: "
	} else {
		labelLabel = "> Label:     "
	}
	b.WriteString(idLabel + inputBoxStyle(m.idInput) + "\n")
	b.WriteString(labelLabel + inputBoxStyle(m.labelInput) + "\n")
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("tab switch field · ↵ next · esc back"))
}

func (m channelsModel) renderOptions(b *strings.Builder) {
	if m.editingID != 0 {
		b.WriteString("  Edit channel gating:\n\n")
	} else {
		b.WriteString(fmt.Sprintf("  Bind %s channel — gating (optional):\n\n", m.selectedProvider.Name))
	}
	mentionLabel := "  Mention @name: "
	sendersLabel := "  Senders:       "
	if m.optField == 0 {
		mentionLabel = "> Mention @name: "
	} else {
		sendersLabel = "> Senders:       "
	}
	senders := "owner only"
	if m.optAllowAnyone {
		senders = "anyone"
	}
	b.WriteString(mentionLabel + inputBoxStyle(m.mentionInput) + "\n")
	b.WriteString(sendersLabel + lipgloss.NewStyle().Foreground(colorPrimary).Render("["+senders+"]") + "\n")
	hint := "empty mention = forward all messages · space toggles senders"
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render(hint))
	b.WriteString("\n  " + lipgloss.NewStyle().Foreground(colorMuted).Render("tab switch field · ↵ save · esc back"))
}

// inputBoxStyle renders a simple bracketed input value with a trailing cursor.
func inputBoxStyle(v string) string {
	return lipgloss.NewStyle().Foreground(colorPrimary).Render(v + "▏")
}
