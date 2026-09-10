package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"james/hem/pkg/protocol"
	"james/moneypenny/pkg/envelope"
)

type permissionTestSender struct {
	request *protocol.Request
}

func (s *permissionTestSender) Send(req *protocol.Request) (*protocol.Response, error) {
	s.request = req
	return protocol.OKResponse(map[string]string{"session_id": "created"}), nil
}

func permissionArgs(args []string) map[string]string {
	result := map[string]string{}
	for i, arg := range args {
		if !strings.HasPrefix(arg, "--gadget-") {
			continue
		}
		key, value, ok := strings.Cut(arg, "=")
		if !ok && i+1 < len(args) {
			value = args[i+1]
		}
		result[key] = value
	}
	return result
}

func TestCreateFormsSendExplicitGadgetPermissions(t *testing.T) {
	for _, wizard := range []bool{false, true} {
		sender := &permissionTestSender{}
		c := &client{sender: sender}
		var cmd tea.Cmd
		if wizard {
			m := newWizardModel(c)
			m.fields[0].value = "hello"
			setGadgetCapabilityFields(m.fields, &envelope.GadgetCapabilities{})
			cmd = m.createSession()
		} else {
			m := newCreateModel(c)
			m.fields[0].value = "hello"
			setGadgetCapabilityFields(m.fields, &envelope.GadgetCapabilities{})
			cmd = m.createSession()
		}
		if msg := cmd().(sessionCreatedMsg); msg.err != nil {
			t.Fatal(msg.err)
		}
		if sender.request.Verb != "create" {
			t.Fatalf("unexpected request: %#v", sender.request)
		}
		got := permissionArgs(sender.request.Args)
		if len(got) != 6 {
			t.Fatalf("wizard=%v omitted permissions: %v", wizard, sender.request.Args)
		}
		for flag, value := range got {
			if value != "false" {
				t.Errorf("%s = %s, want explicit false", flag, value)
			}
		}
	}
}

func TestWizardCopiesGadgetPermissions(t *testing.T) {
	sender := &permissionTestSender{}
	m := newWizardModel(&client{sender: sender})
	m.sourceSessionID = "source"
	caps := envelope.GadgetCapabilities{Agents: true, Traits: true, CreateAgents: true}
	m, _ = m.Update(wizardSourceLoadedMsg{source: &sessionDetail{
		SessionID: "source", Agent: "copilot", GadgetCapabilities: &caps,
	}})
	if msg := m.createSession()().(sessionCreatedMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	if sender.request.Verb != "copy" {
		t.Fatalf("unexpected request: %#v", sender.request)
	}
	args := permissionArgs(sender.request.Args)
	if args["--gadget-agents"] != "true" || args["--gadget-traits"] != "true" || args["--gadget-memory"] != "false" ||
		args["--gadget-subagents"] != "false" || args["--gadget-scheduling"] != "false" || args["--gadget-create-agents"] != "true" {
		t.Fatalf("copy lost permissions: %v", args)
	}
}

func TestEditFormLoadsAndUpdatesGadgetPermissions(t *testing.T) {
	for _, caps := range []*envelope.GadgetCapabilities{nil, {}, {Traits: true, CreateAgents: true}} {
		sender := &permissionTestSender{}
		m := newEditModel(&client{sender: sender}, "session")
		m, _ = m.Update(sessionDetailLoadedMsg{detail: &sessionDetail{GadgetCapabilities: caps}})
		want := gadgetCapabilityFields(caps)
		for _, field := range want {
			for _, got := range m.fields {
				if field.flag == got.flag && field.value != got.value {
					t.Errorf("loaded %s=%s, want %s", got.flag, got.value, field.value)
				}
			}
		}
		if msg := m.save()().(sessionUpdatedMsg); msg.err != nil {
			t.Fatal(msg.err)
		}
		if sender.request != nil {
			t.Fatal("unchanged form sent an update")
		}
		for i := range m.fields {
			if m.fields[i].flag == "--gadget-traits" {
				m.cursor = i
			}
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
		if msg := m.save()().(sessionUpdatedMsg); msg.err != nil {
			t.Fatal(msg.err)
		}
		if sender.request == nil || sender.request.Verb != "update" {
			t.Fatalf("missing update: %#v", sender.request)
		}
		args := permissionArgs(sender.request.Args)
		wantValue := "true"
		if caps != nil && caps.Traits {
			wantValue = "false"
		}
		if len(args) != 1 || args["--gadget-traits"] != wantValue {
			t.Fatalf("edit did not preserve omitted settings: %v", args)
		}
	}
}

func TestPermissionFormsKeepSelectedFieldVisible(t *testing.T) {
	m := newEditModel(nil, "session")
	m, _ = m.Update(sessionDetailLoadedMsg{detail: &sessionDetail{}})
	m.height = 24
	m.cursor = len(m.fields) - 1
	if rendered := m.View(); !strings.Contains(rendered, "scheduling") {
		t.Fatalf("last permission is not visible: %s", rendered)
	}
	rows := []string{"first\n", "second\nmultiline\n", "selected\n", "last\n"}
	got := formViewport(rows, 2, 2)
	if !strings.Contains(got, "selected") || strings.Count(got, "\n") > 2 {
		t.Fatalf("viewport did not keep selected field in bounds: %q", got)
	}
}
