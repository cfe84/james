package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/commands"
	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/moneypenny/pkg/envelope"
)

func TestPonsonbiesManagement(t *testing.T) {
	sender := &permissionTestSender{response: protocol.OKResponse(commands.TableResult{Rows: [][]string{{"store-id", "engineering", "Decisions", "host", "yes", "copilot", "cheap"}}})}
	c := &client{sender: sender}
	parent := Model{client: c, currentView: viewDashboard}
	model, cmd := parent.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	parent = model.(Model)
	if parent.currentView != viewPonsonbies || cmd == nil {
		t.Fatal("dashboard shortcut did not open stores")
	}
	model, _ = parent.Update(cmd())
	parent = model.(Model)
	m := parent.ponsonbies
	if len(m.stores) != 1 || !m.stores[0].Default || !strings.Contains(m.View(), "engineering") {
		t.Fatal("list not loaded", m)
	}
	sender.response = protocol.ErrResponse("host unavailable")
	for _, editing := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "edit"}[editing], func(t *testing.T) {
			var source *store.Ponsonby
			if editing {
				source = &m.stores[0]
			}
			form := m.edit(source)
			for i := range form.fields {
				switch form.fields[i].flag {
				case "--name":
					form.fields[i].value = "new-store"
				case "--purpose":
					form.fields[i].value = "Shared decisions"
				case "--moneypenny":
					form.fields[i].value = "host"
				case "--model":
					form.fields[i].value = "cheap"
				case "--default":
					form.fields[i].value = "false"
				}
			}
			m.form = form
			result := form.createSession()()
			m, _ = m.Update(result)
			wantVerb := "create"
			if editing {
				wantVerb = "update"
			}
			req := sender.request
			if req.Verb != wantVerb || req.Noun != "ponsonby" {
				t.Fatal(req)
			}
			args := strings.Join(req.Args, " ")
			if !strings.Contains(args, "--default=false") || !strings.Contains(args, "--model cheap") {
				t.Fatal("lost form settings", args)
			}
			if editing && (strings.Contains(args, "--name") || strings.Contains(args, "--moneypenny")) {
				t.Fatal("edit attempted immutable settings", args)
			}
			if m.form == nil || m.form.creating || m.form.err == nil {
				t.Fatal("failed save lost form or error")
			}
			sender.response = protocol.OKResponse(nil)
			m, cmd = m.Update(form.createSession()())
			if m.form != nil || cmd == nil {
				t.Fatal("successful save did not return to list")
			}
			sender.response = protocol.ErrResponse("host unavailable")
		})
	}
	sender.response = protocol.ErrResponse("remove assignments before deletion")
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !m.confirmDelete || cmd != nil {
		t.Fatal("delete did not require confirmation")
	}
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m, _ = m.Update(cmd())
	if m.err == nil || len(m.stores) != 1 {
		t.Fatal("delete failure hid data/error")
	}
	sender.response = protocol.OKResponse(nil)
	m.detail = &store.Ponsonby{PonsonbyStore: envelope.PonsonbyStore{ID: "store-id", Jobs: []envelope.PonsonbyJob{{ID: "failed-job", Status: "failed"}}}}
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	cmd()
	if sender.request.Verb != "retry" || strings.Join(sender.request.Args, " ") != "store-id --request failed-job" {
		t.Fatal("wrong failed request retried", sender.request)
	}
	parent.ponsonbies = m
	parent.ponsonbies.form = m.edit(nil)
	model, _ = parent.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if model.(Model).currentView != viewPonsonbies || model.(Model).ponsonbies.form != nil {
		t.Fatal("escape did not cancel form")
	}
}
