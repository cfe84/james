package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/commands"
	"james/moneypenny/pkg/envelope"
)

func attentionLabel(kind string) string {
	switch kind {
	case "needs_answer":
		return "Needs answer"
	case "needs_action":
		return "Needs action"
	case "failed":
		return "Failed"
	}
	return "Attention"
}

func attentionDescription(attention *envelope.SessionAttention) string {
	return attentionLabel(attention.Kind) + ": " + strings.Join(strings.Fields(attention.Reason), " ")
}

func (m *Model) observeAttention(sessionID, name string, attention *envelope.SessionAttention) {
	if attention == nil {
		return
	}
	if m.attentionRevisions == nil {
		m.attentionRevisions = make(map[string]int64)
	}
	if attention.Revision <= m.attentionRevisions[sessionID] {
		return
	}
	m.attentionRevisions[sessionID] = attention.Revision
	if !attention.Active() {
		return
	}
	m.statusMsg = truncate(name+" · "+attentionDescription(attention), max(20, m.width/2))
	if !m.silent {
		go commands.PlayNotificationSound()
	}
}

type attentionResolvedMsg struct {
	sessionID string
	err       error
}

type attentionLoadedMsg struct {
	sessionID string
	detail    *sessionDetail
	err       error
}

func (m Model) loadAttention(sessionID string) tea.Cmd {
	return func() tea.Msg {
		detail, err := m.client.showSession(sessionID)
		return attentionLoadedMsg{sessionID: sessionID, detail: detail, err: err}
	}
}

func (m chatModel) resolveAttention() tea.Cmd {
	sessionID, revision := m.sessionID, m.attention.Revision
	return func() tea.Msg {
		resp, err := m.client.send("resolve", "attention", sessionID, "--revision", fmt.Sprint(revision))
		if err == nil && resp.Status == "error" {
			err = fmt.Errorf("%s", resp.Message)
		}
		return attentionResolvedMsg{sessionID: sessionID, err: err}
	}
}
