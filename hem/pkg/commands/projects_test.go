package commands

import (
	"encoding/json"
	"testing"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
)

func TestProjectAgentCounts(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := &Executor{store: st}
	if err := st.AddMoneypenny(&store.Moneypenny{Name: "offline", TransportType: store.TransportFIFO}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Empty", "Work", "Paused", "Done"} {
		if resp := e.CreateProject([]string{"--name", name}); resp.Status != protocol.StatusOK {
			t.Fatalf("create: %+v", resp)
		}
	}
	for name, status := range map[string]string{"Paused": "paused", "Done": "done"} {
		if resp := e.UpdateProject([]string{name, "--status", status}); resp.Status != protocol.StatusOK {
			t.Fatalf("update: %+v", resp)
		}
	}
	work, err := st.GetProject("Work")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.TrackSession("parent", "offline", work.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.TrackSubSession("child", "offline", "parent", work.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.TrackSession("unassigned", "offline"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionHemStatus("child", "completed"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		status, active, completed, display string
	}{
		{"active", "1", "1", "active"},
		{"completed", "0", "2", "stale"},
		{"active", "1", "1", "active"},
	} {
		if err := st.SetSessionHemStatus("parent", step.status); err != nil {
			t.Fatal(err)
		}
		response := e.ListProjects(nil)
		var table TableResult
		if err := json.Unmarshal(response.Data, &table); err != nil || response.Status != protocol.StatusOK {
			t.Fatalf("list: %+v, %v", response, err)
		}
		if len(table.Rows) != 4 || len(table.Headers) != 10 {
			t.Fatalf("unexpected project table: %+v", table)
		}
		for _, row := range table.Rows {
			active, completed, display, saved := "0", "0", "stale", "active"
			switch row[1] {
			case "Work":
				active, completed, display = step.active, step.completed, step.display
			case "Paused":
				saved, display = "paused", "paused"
			case "Done":
				saved, display = "done", "done"
			}
			if row[2] != saved || row[7] != active || row[8] != completed || row[9] != display {
				t.Fatalf("parent %s: unexpected row %v", step.status, row)
			}
		}
		filtered := e.ListProjects([]string{"--status=active"})
		if err := json.Unmarshal(filtered.Data, &table); err != nil || len(table.Rows) != 2 {
			t.Fatalf("saved-status filter: %+v, %v", filtered, err)
		}
		shown := e.ShowProject([]string{"Work"})
		var project ProjectResult
		if err := json.Unmarshal(shown.Data, &project); err != nil || project.Status != "active" {
			t.Fatalf("saved project status changed: %+v, %v", shown, err)
		}
	}
}

func TestProjectCategoryCommands(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := &Executor{store: st}
	if resp := e.CreateProject([]string{"--name", "Example", "--category", " Work "}); resp.Status != protocol.StatusOK {
		t.Fatalf("create: %+v", resp)
	}
	for _, category := range []string{"Work", "Personal", ""} {
		if category != "Work" {
			if resp := e.UpdateProject([]string{"Example", "--category=" + category}); resp.Status != protocol.StatusOK {
				t.Fatalf("update category: %+v", resp)
			}
			for _, args := range [][]string{{"--display=false"}, {"--override-filter=true"}, {"--name=Work"}, {"--name=Work", "--display=maybe"}, {"--name=Work", "--override-filter=maybe"}, {"--name=Work", "--override-filter=true", "extra"}} {
				if resp := e.UpdateCategory(args); resp.Status != protocol.StatusError {
					t.Fatalf("invalid category setting accepted: %+v", resp)
				}
			}
			for _, category := range []string{"Work", ""} {
				for _, display := range []bool{false, true} {
					value := "false"
					if display {
						value = "true"
					}
					resp := e.UpdateCategory([]string{"--name=" + category, "--display=" + value})
					if resp.Status != protocol.StatusOK {
						t.Fatalf("category default update: %+v", resp)
					}
					if resp := e.UpdateCategory([]string{"--name=" + category, "--override-filter=" + value}); resp.Status != protocol.StatusOK {
						t.Fatalf("category override update: %+v", resp)
					}
					var listed struct {
						CategoryDefaults        map[string]bool `json:"category_defaults"`
						CategoryFilterOverrides map[string]bool `json:"category_filter_overrides"`
					}
					if err := json.Unmarshal(e.ListProjects(nil).Data, &listed); err != nil {
						t.Fatal(err)
					}
					if got, exists := listed.CategoryDefaults[category]; !exists || got != display {
						t.Fatalf("category defaults = %+v", listed.CategoryDefaults)
					}
					if got, exists := listed.CategoryFilterOverrides[category]; !exists || got != display {
						t.Fatalf("category overrides = %+v", listed.CategoryFilterOverrides)
					}
				}
			}
		}
		if resp := e.UpdateProject([]string{"Example", "--status", "paused"}); resp.Status != protocol.StatusOK {
			t.Fatalf("unrelated update: %+v", resp)
		}
		shown := e.ShowProject([]string{"Example"})
		var project ProjectResult
		if err := json.Unmarshal(shown.Data, &project); err != nil || project.Category != category || project.Status != "paused" {
			t.Fatalf("show: %+v, %v", shown, err)
		}
		listed := e.ListProjects(nil)
		var table TableResult
		if err := json.Unmarshal(listed.Data, &table); err != nil || len(table.Rows) != 1 || len(table.Headers) < 7 || table.Headers[6] != "Category" || table.Rows[0][6] != category {
			t.Fatalf("list: %+v, %v", listed, err)
		}
	}
}
