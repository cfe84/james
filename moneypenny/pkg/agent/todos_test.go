package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"james/moneypenny/pkg/envelope"
)

func TestTodoDatabaseURI(t *testing.T) {
	for path, want := range map[string]string{
		"C:/User Name/#todo/session.db": "file:///C:/User%20Name/%23todo/session.db?mode=ro&_busy_timeout=250",
		"/home/user/session.db":         "file:///home/user/session.db?mode=ro&_busy_timeout=250",
	} {
		if got := todoDatabaseURI(path); got != want {
			t.Errorf("todoDatabaseURI(%q) = %q; want %q", path, got, want)
		}
	}
}

func TestReadTodoDatabase(t *testing.T) {
	for _, tc := range []struct {
		provider string
		fixture  string
	}{
		{"copilot", `CREATE TABLE todos(title TEXT, description TEXT, status TEXT, created_at TEXT);
			INSERT INTO todos VALUES ('','execute','in_progress','2026-01-02'),
				('plan','details','done','2026-01-01');`},
		{"opencode", `CREATE TABLE todo(session_id TEXT, content TEXT, status TEXT, position INTEGER);
			INSERT INTO todo VALUES ('ses_test','execute','in_progress',1),
				('ses_test','plan','completed',0),('ses_other','unrelated','pending',0);`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks #1.db")
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tc.fixture); err != nil {
				t.Fatal(err)
			}
			got, err := readTodoDatabase(context.Background(), path, tc.provider, "ses_test")
			want := []envelope.SessionTodo{{Content: "plan", Status: "done"}, {Content: "execute", Status: "in_progress"}}
			if err != nil || !reflect.DeepEqual(got.Items, want) {
				t.Fatalf("readTodoDatabase = %+v, %v; want %+v", got, err, want)
			}
			if _, err := readTodoDatabase(context.Background(), path+".missing", tc.provider, "ses_test"); err == nil {
				t.Fatal("missing database was treated as available")
			}
		})
	}
}
