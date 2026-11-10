package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/envelope"
)

func TestChatAttentionPersistsUntilExplicitRevisionResolution(t *testing.T) {
	request := &envelope.SessionAttention{Kind: "needs_answer", Reason: "Which region?", Revision: 2}
	sender := &permissionTestSender{response: protocol.OKResponse(envelope.SessionReport{
		Attention: &envelope.SessionAttention{Revision: 3},
	})}
	m := Model{client: &client{sender: sender}, silent: true, currentView: viewChat}
	m.chat = newChatModel(m.client, "session", "Deploy", "mac")
	m.chat.width, m.chat.height = 80, 24
	m.chat.commandMode = true
	for _, attention := range []*envelope.SessionAttention{request, request, {Kind: "needs_action", Reason: "Older request", Revision: 1}} {
		updated, _ := m.Update(historyLoadedMsg{sessionID: "session", report: envelope.SessionReport{Attention: attention}})
		m = updated.(Model)
	}
	if m.attentionRevisions["session"] != 2 || m.chat.attention != request ||
		!strings.Contains(m.chat.View(), "Needs answer: Which region?") {
		t.Fatalf("read/repeat/stale metadata changed request: %+v", m.chat.attention)
	}
	m.statusMsg = "unchanged"
	m.observeAttention("session", "Deploy", request)
	if m.statusMsg != "unchanged" {
		t.Fatal("refresh replayed notification")
	}
	updated, cmd := m.updateChat(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("resolve shortcut did not dispatch")
	}
	resolved := cmd().(attentionResolvedMsg)
	if resolved.err != nil || sender.request.Verb != "resolve" || sender.request.Noun != "attention" ||
		strings.Join(sender.request.Args, " ") != "session --revision 2" {
		t.Fatalf("incorrect revision resolution: request=%+v err=%v", sender.request, resolved.err)
	}
	m.chat, _ = m.chat.Update(historyLoadedMsg{
		sessionID: "session", report: envelope.SessionReport{Attention: &envelope.SessionAttention{Revision: 3}},
	})
	if m.chat.attention.Active() || strings.Contains(m.chat.View(), "Which region?") {
		t.Fatal("resolved banner remained visible")
	}
	m.observeAttention("session", "Deploy", m.chat.attention)
	if m.statusMsg != "unchanged" || m.attentionRevisions["session"] != 3 {
		t.Fatal("resolution generated another alert")
	}
}
