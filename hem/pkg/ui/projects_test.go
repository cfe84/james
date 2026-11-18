package ui

import (
	"strings"
	"testing"

	"james/hem/pkg/commands"
	"james/hem/pkg/protocol"
)

func TestProjectListActivity(t *testing.T) {
	sender := &permissionTestSender{response: protocol.OKResponse(commands.TableResult{Rows: [][]string{
		{"work", "Work", "active", "", "", "[]", "Background", "3", "1", "active"},
		{"empty", "Empty", "active", "", "", "[]", "", "0", "2", "stale"},
		{"old", "Older server", "paused", "", "", "[]"},
	}})}
	c := &client{sender: sender}
	m := newProjectsModel(c)
	m, _ = m.Update(m.loadProjects()())
	view := m.View()
	for _, text := range []string{"Active", "Completed", "stale", "Older server", "paused", "Uncategorized", "Background"} {
		if !strings.Contains(view, text) {
			t.Fatalf("missing %q in project view: %s", text, view)
		}
	}
	if m.projects[1].Active != "0" || m.projects[1].Completed != "2" || m.projects[0].Active != "3" {
		t.Fatalf("lost activity counts: %+v", m.projects)
	}
	if strings.Index(view, "Background") > strings.Index(view, "Uncategorized") {
		t.Fatal("server category ordering was not preserved", view)
	}
	m.cursor, m.height = 0, 8
	if !strings.Contains(m.View(), "Work") || m.selectedProject().ID != "work" {
		t.Fatal("group headings hid or changed selected project", m.View())
	}
	if !strings.Contains(m.View(), "showing 1-2 of 3") {
		t.Fatal("group headings must count toward viewport pagination", m.View())
	}
	edit := newEditProjectModel(c, &m.projects[1])
	if edit.fields[1].value != "active" {
		t.Fatalf("edit must use saved status, not stale: %+v", edit.fields)
	}
}
