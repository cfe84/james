package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"james/moneypenny/pkg/envelope"
)

// ReadSessionTodos reads the provider's current task list without changing it.
func (r *Runner) ReadSessionTodos(ctx context.Context, params RunParams) (envelope.SessionTodos, error) {
	id := params.AgentSessionID
	var path string
	switch params.Agent {
	case "copilot":
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\:`) {
			return envelope.SessionTodos{}, errors.New("invalid Copilot session ID")
		}
		home, overridden := params.Environment["COPILOT_HOME"]
		if !overridden {
			home = os.Getenv("COPILOT_HOME")
		}
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return envelope.SessionTodos{}, err
			}
			home = filepath.Join(userHome, ".copilot")
		}
		if !filepath.IsAbs(home) {
			home = filepath.Join(params.Path, home)
		}
		path = filepath.Join(home, "session-state", id, "session.db")
	case "opencode":
		r.mu.Lock()
		buf := r.activity[params.SessionID]
		r.mu.Unlock()
		if buf != nil {
			if liveID, ok := buf.openCodeSessionID.Load().(string); ok {
				id = liveID
			}
		}
		if id == "" {
			return envelope.SessionTodos{}, errors.New("OpenCode has not reported its session ID")
		}
		binary, err := FindAgent(params.Agent)
		if err != nil {
			return envelope.SessionTodos{}, err
		}
		cmd := exec.CommandContext(ctx, binary, "db", "path")
		cmd.Dir = params.Path
		cmd.Env = PrependToPath(withEnvironment(os.Environ(), params.Environment), filepath.Dir(binary))
		output, err := cmd.Output()
		if err != nil {
			return envelope.SessionTodos{}, fmt.Errorf("resolve OpenCode database: %w", err)
		}
		path = strings.TrimSpace(string(output))
	default:
		return envelope.SessionTodos{}, errors.New("unsupported todo provider")
	}
	return readTodoDatabase(ctx, path, params.Agent, id)
}

func readTodoDatabase(ctx context.Context, path, provider, id string) (envelope.SessionTodos, error) {
	if !filepath.IsAbs(path) {
		return envelope.SessionTodos{}, errors.New("provider database path is not absolute")
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro&_busy_timeout=250"}
	db, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return envelope.SessionTodos{}, err
	}
	defer db.Close()

	const limit = 100
	var rows *sql.Rows
	if provider == "copilot" {
		rows, err = db.QueryContext(ctx, `SELECT substr(COALESCE(NULLIF(title, ''), description, ''), 1, 512), substr(COALESCE(status, 'pending'), 1, 32)
			FROM todos ORDER BY created_at, rowid LIMIT ?`, limit+1)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT substr(content, 1, 512), substr(status, 1, 32)
			FROM todo WHERE session_id = ? ORDER BY position LIMIT ?`, id, limit+1)
	}
	if err != nil {
		return envelope.SessionTodos{}, err
	}
	defer rows.Close()
	result := envelope.SessionTodos{Items: []envelope.SessionTodo{}}
	for rows.Next() {
		if len(result.Items) == limit {
			result.Truncated = true
			break
		}
		var item envelope.SessionTodo
		if err := rows.Scan(&item.Content, &item.Status); err != nil {
			return envelope.SessionTodos{}, err
		}
		if item.Status == "completed" {
			item.Status = "done"
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
