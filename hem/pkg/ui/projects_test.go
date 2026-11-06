package ui

import (
	"strings"
	"testing"

	"james/hem/pkg/commands"
	"james/hem/pkg/protocol"
)

func TestProjectListActivity(t *testing.T) {
	sender := &permissionTestSender{response: protocol.OKResponse(commands.TableResult{Rows: [][]string{
		{"empty", "Empty", "active", "", "", "[]", "", "0", "2", "stale"},
		{"work", "Work", "active", "", "", "[]", "Background", "3", "1", "active"},
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
	if m.projects[0].Active != "0" || m.projects[0].Completed != "2" || m.projects[2].Active != "3" {
		t.Fatalf("lost activity counts: %+v", m.projects)
	}
	if strings.Index(view, "Older server") > strings.Index(view, "Background") {
		t.Fatal("projects were not grouped by category", view)
	}
	m.cursor, m.height = 2, 8
	if !strings.Contains(m.View(), "Work") || m.selectedProject().ID != "work" {
		t.Fatal("group headings hid or changed selected project", m.View())
	}
	if !strings.Contains(m.View(), "showing 2-3 of 3") {
		t.Fatal("group headings must count toward viewport pagination", m.View())
	}
	edit := newEditProjectModel(c, &m.projects[0])
	if edit.fields[1].value != "active" {
		t.Fatalf("edit must use saved status, not stale: %+v", edit.fields)
	}
}
