package ui

import (
	"encoding/json"
	"fmt"

	"james/hem/pkg/hemclient"
	"james/hem/pkg/protocol"
	"james/hem/pkg/server"
)

// client wraps hemclient for the UI, providing typed methods.
type client struct {
	sender hemclient.Sender
}

func newClient() *client {
	return &client{sender: &hemclient.SocketSender{SockPath: server.DefaultSocketPath()}}
}

func newMI6Client(sender hemclient.Sender) *client {
	return &client{sender: sender}
}

func (c *client) send(verb, noun string, args ...string) (*protocol.Response, error) {
	req := &protocol.Request{Verb: verb, Noun: noun, Args: args}
	return c.sender.Send(req)
}

// sessionInfo is a parsed session from list sessions.
type sessionInfo struct {
	SessionID    string `json:"session_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Moneypenny   string `json:"-"` // filled in by caller
	CreatedAt    string `json:"created_at"`
	LastAccessed string `json:"last_accessed"`
}

// sessionDetail is a parsed session detail.
type sessionDetail struct {
	SessionID    string             `json:"session_id"`
	Moneypenny   string             `json:"moneypenny"`
	Name         string             `json:"name"`
	Agent        string             `json:"agent"`
	SystemPrompt string             `json:"system_prompt"`
	Model        string             `json:"model"`
	Yolo         bool               `json:"yolo"`
	Gadgets      bool               `json:"gadgets"`
	Path         string             `json:"path"`
	Status       string             `json:"status"`
	Conversation []conversationTurn `json:"conversation"`
}

type conversationTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Queued    bool   `json:"-"` // local-only flag for optimistic UI
}

func (c *client) listSessions(mpFilter string) ([]sessionInfo, error) {
	args := []string{}
	if mpFilter != "" {
		args = append(args, "-m", mpFilter)
	}
	resp, err := c.send("list", "session", args...)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	// The response is a TableResult. Parse the raw data.
	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing sessions: %w", err)
	}

	var sessions []sessionInfo
	for _, row := range table.Rows {
		s := sessionInfo{}
		if len(row) > 0 {
			s.SessionID = row[0]
		}
		if len(row) > 1 {
			s.Name = row[1]
		}
		if len(row) > 2 {
			s.Status = row[2]
		}
		if len(row) > 3 {
			s.Moneypenny = row[3]
		}
		if len(row) > 4 {
			s.CreatedAt = row[4]
		}
		if len(row) > 5 {
			s.LastAccessed = row[5]
		}
		sessions = append(sessions, s)
	}
	return sessions, nil
}

func (c *client) showSession(sessionID string) (*sessionDetail, error) {
	resp, err := c.send("show", "session", sessionID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var detail sessionDetail
	if err := json.Unmarshal(resp.Data, &detail); err != nil {
		return nil, fmt.Errorf("parsing session: %w", err)
	}
	return &detail, nil
}

type historyPage struct {
	Conversation []conversationTurn
	Total        int
}

func (c *client) getHistory(sessionID string) ([]conversationTurn, error) {
	page, err := c.getHistoryPaginated(sessionID, 0, 0)
	if err != nil {
		return nil, err
	}
	return page.Conversation, nil
}

func (c *client) getHistoryPaginated(sessionID string, count, from int) (*historyPage, error) {
	args := []string{sessionID}
	if count > 0 {
		args = append(args, "--count", fmt.Sprintf("%d", count))
		args = append(args, "--from", fmt.Sprintf("%d", from))
	}
	resp, err := c.send("history", "session", args...)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var result struct {
		Conversation []conversationTurn `json:"conversation"`
		Total        int                `json:"total"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("parsing history: %w", err)
	}
	return &historyPage{Conversation: result.Conversation, Total: result.Total}, nil
}

func (c *client) createSession(args []string) (sessionID string, response string, err error) {
	resp, err := c.send("create", "session", args...)
	if err != nil {
		return "", "", err
	}
	if resp.Status == protocol.StatusError {
		return "", "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		SessionID string `json:"session_id"`
		Response  string `json:"response"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", "", err
	}
	return result.SessionID, result.Response, nil
}

type continueResult struct {
	Response string
	Queued   bool
}

func (c *client) continueSession(sessionID, prompt string) (continueResult, error) {
	resp, err := c.send("continue", "session", sessionID, "--async", prompt)
	if err != nil {
		return continueResult{}, err
	}
	if resp.Status == protocol.StatusError {
		return continueResult{}, fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Response string `json:"response"`
		Queued   bool   `json:"queued"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return continueResult{}, err
	}
	return continueResult{Response: result.Response, Queued: result.Queued}, nil
}

func (c *client) updateSession(sessionID string, fields map[string]string) error {
	args := []string{sessionID}
	for flag, value := range fields {
		args = append(args, flag, value)
	}
	resp, err := c.send("update", "session", args...)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) deleteSession(sessionID string) error {
	resp, err := c.send("delete", "session", sessionID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) stopSession(sessionID string) error {
	resp, err := c.send("stop", "session", sessionID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// projectInfo is a parsed project from list projects.
type projectInfo struct {
	ID         string
	Name       string
	Status     string
	Moneypenny string
	Agent      string
	Paths      string
}

func (c *client) listProjects(statusFilter string) ([]projectInfo, error) {
	args := []string{}
	if statusFilter != "" {
		args = append(args, "--status", statusFilter)
	}
	resp, err := c.send("list", "project", args...)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing projects: %w", err)
	}

	var projects []projectInfo
	for _, row := range table.Rows {
		p := projectInfo{}
		if len(row) > 0 {
			p.ID = row[0]
		}
		if len(row) > 1 {
			p.Name = row[1]
		}
		if len(row) > 2 {
			p.Status = row[2]
		}
		if len(row) > 3 {
			p.Moneypenny = row[3]
		}
		if len(row) > 4 {
			p.Agent = row[4]
		}
		if len(row) > 5 {
			p.Paths = row[5]
		}
		projects = append(projects, p)
	}
	return projects, nil
}

type projectDetail struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Status              string `json:"status"`
	Moneypenny          string `json:"moneypenny"`
	Paths               string `json:"paths"`
	DefaultAgent        string `json:"default_agent"`
	DefaultSystemPrompt string `json:"default_system_prompt"`
}

func (c *client) showProject(nameOrID string) (*projectDetail, error) {
	resp, err := c.send("show", "project", nameOrID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	var detail projectDetail
	if err := json.Unmarshal(resp.Data, &detail); err != nil {
		return nil, fmt.Errorf("parsing project: %w", err)
	}
	return &detail, nil
}

func (c *client) deleteProject(nameOrID string) error {
	resp, err := c.send("delete", "project", nameOrID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) createProject(args []string) error {
	resp, err := c.send("create", "project", args...)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) updateProject(nameOrID string, fields map[string]string) error {
	args := []string{nameOrID}
	for flag, value := range fields {
		args = append(args, flag, value)
	}
	resp, err := c.send("update", "project", args...)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

// moneypennyInfo is a parsed moneypenny from list moneypennies.
type moneypennyInfo struct {
	Name      string
	Type      string
	Address   string
	IsDefault bool
	Enabled   bool
}

func (c *client) listMoneypennies() ([]moneypennyInfo, error) {
	resp, err := c.send("list", "moneypenny")
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing moneypennies: %w", err)
	}

	var mps []moneypennyInfo
	for _, row := range table.Rows {
		mp := moneypennyInfo{Enabled: true} // default enabled for backwards compat
		if len(row) > 0 {
			mp.Name = row[0]
		}
		if len(row) > 1 {
			mp.Type = row[1]
		}
		if len(row) > 2 {
			mp.Address = row[2]
		}
		if len(row) > 3 {
			mp.IsDefault = row[3] == "*"
		}
		if len(row) > 4 {
			mp.Enabled = row[4] != "false"
		}
		mps = append(mps, mp)
	}
	return mps, nil
}

func (c *client) addMoneypenny(args []string) error {
	resp, err := c.send("add", "moneypenny", args...)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) pingMoneypenny(name string) (string, error) {
	resp, err := c.send("ping", "moneypenny", "-n", name)
	if err != nil {
		return "", err
	}
	if resp.Status == protocol.StatusError {
		return "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", err
	}
	return result.Message, nil
}

func (c *client) deleteMoneypenny(name string) error {
	resp, err := c.send("delete", "moneypenny", "-n", name)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) enableMoneypenny(name string, enabled bool) error {
	verb := "enable"
	if !enabled {
		verb = "disable"
	}
	resp, err := c.send(verb, "moneypenny", "-n", name)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) setDefaultMoneypenny(name string) error {
	resp, err := c.send("set-default", "moneypenny", "-n", name)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

type runCommandResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func (c *client) runCommand(moneypenny, path, command string) (runCommandResult, error) {
	args := []string{}
	if moneypenny != "" {
		args = append(args, "-m", moneypenny)
	}
	if path != "" {
		args = append(args, "--path", path)
	}
	args = append(args, command)
	resp, err := c.send("run", "", args...)
	if err != nil {
		return runCommandResult{}, err
	}
	if resp.Status == protocol.StatusError {
		return runCommandResult{}, fmt.Errorf("%s", resp.Message)
	}
	var result runCommandResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return runCommandResult{}, fmt.Errorf("parsing result: %w", err)
	}
	return result, nil
}

type dirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

func (c *client) listDirectory(moneypenny, path string) ([]dirEntry, error) {
	args := []string{}
	if moneypenny != "" {
		args = append(args, "-m", moneypenny)
	}
	if path != "" {
		args = append(args, "--path", path)
	}
	resp, err := c.send("list-directory", "", args...)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Path    string     `json:"path"`
		Entries []dirEntry `json:"entries"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("parsing directory: %w", err)
	}
	return result.Entries, nil
}

type activityEvent struct {
	Type      string `json:"type"`
	Summary   string `json:"summary"`
	Timestamp string `json:"timestamp"`
}

func (c *client) getSessionActivity(sessionID string) ([]activityEvent, error) {
	resp, err := c.send("activity", "session", sessionID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Activity []activityEvent `json:"activity"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("parsing activity: %w", err)
	}
	return result.Activity, nil
}

type scheduleInfo struct {
	ID          int64  `json:"id"`
	SessionID   string `json:"session_id"`
	Prompt      string `json:"prompt"`
	ScheduledAt string `json:"scheduled_at"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

func (c *client) listSchedules(sessionID string) ([]scheduleInfo, error) {
	resp, err := c.send("list", "schedule", "--session-id", sessionID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	// Check if it's a text result (no schedules found).
	var text struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(resp.Data, &text) == nil && text.Message != "" {
		return nil, nil
	}

	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing schedules: %w", err)
	}

	var schedules []scheduleInfo
	for _, row := range table.Rows {
		s := scheduleInfo{}
		if len(row) > 0 {
			fmt.Sscanf(row[0], "%d", &s.ID)
		}
		if len(row) > 1 {
			s.Status = row[1]
		}
		if len(row) > 2 {
			s.ScheduledAt = row[2]
		}
		if len(row) > 3 {
			s.Prompt = row[3]
		}
		s.SessionID = sessionID
		schedules = append(schedules, s)
	}
	return schedules, nil
}

func (c *client) scheduleSession(sessionID, at, prompt string) error {
	resp, err := c.send("schedule", "session", sessionID, "--at", at, "--prompt", prompt)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) cancelSchedule(sessionID string, scheduleID int64) error {
	resp, err := c.send("cancel", "schedule", fmt.Sprintf("%d", scheduleID), "--session-id", sessionID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) listSubagents(parentSessionID string) ([]struct {
	SessionID string
	Name      string
	Status    string
}, error) {
	resp, err := c.send("list", "subsession", parentSessionID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing subagents: %w", err)
	}

	var result []struct {
		SessionID string
		Name      string
		Status    string
	}
	for _, row := range table.Rows {
		entry := struct {
			SessionID string
			Name      string
			Status    string
		}{}
		if len(row) > 0 {
			entry.SessionID = row[0]
		}
		if len(row) > 1 {
			entry.Name = row[1]
		}
		if len(row) > 2 {
			entry.Status = row[2]
		}
		result = append(result, entry)
	}
	return result, nil
}

func (c *client) createSubagent(parentSessionID, prompt string) (string, string, error) {
	resp, err := c.send("create", "subsession", parentSessionID, "--async", "--yolo", prompt)
	if err != nil {
		return "", "", err
	}
	if resp.Status == protocol.StatusError {
		return "", "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", "", fmt.Errorf("parsing subsession: %w", err)
	}
	return result.SessionID, result.Name, nil
}

func (c *client) moveSessionToProject(sessionID, projectNameOrID string) error {
	resp, err := c.send("update", "session", sessionID, "--project", projectNameOrID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

type templateInfo struct {
	ID      string
	Name    string
	Project string // only populated in global listing
	Agent   string
	Path    string
	Prompt  string
}

func (c *client) listTemplates(projectName string) ([]templateInfo, error) {
	var resp *protocol.Response
	var err error
	if projectName != "" {
		resp, err = c.send("list", "template", "--project", projectName)
	} else {
		resp, err = c.send("list", "template")
	}
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	var table struct {
		Headers []string   `json:"headers"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.Unmarshal(resp.Data, &table); err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	// Detect column layout from headers.
	hasProject := len(table.Headers) > 2 && table.Headers[2] == "Project"

	var templates []templateInfo
	for _, row := range table.Rows {
		t := templateInfo{}
		if len(row) > 0 {
			t.ID = row[0]
		}
		if len(row) > 1 {
			t.Name = row[1]
		}
		if hasProject {
			// Global: ID, Name, Project, Agent, Path, Prompt, Yolo
			if len(row) > 2 {
				t.Project = row[2]
			}
			if len(row) > 3 {
				t.Agent = row[3]
			}
			if len(row) > 4 {
				t.Path = row[4]
			}
			if len(row) > 5 {
				t.Prompt = row[5]
			}
		} else {
			// Per-project: ID, Name, Agent, Path, Prompt, Yolo
			if len(row) > 2 {
				t.Agent = row[2]
			}
			if len(row) > 3 {
				t.Path = row[3]
			}
			if len(row) > 4 {
				t.Prompt = row[4]
			}
		}
		templates = append(templates, t)
	}
	return templates, nil
}

func (c *client) createTemplate(args []string) error {
	resp, err := c.send("create", "template", args...)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) deleteTemplate(nameOrID, projectName string) error {
	resp, err := c.send("delete", "template", nameOrID, "--project", projectName)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) useTemplate(templateNameOrID, projectName string) (string, error) {
	args := []string{templateNameOrID, "--async"}
	if projectName != "" {
		args = append(args, "--project", projectName)
	}
	resp, err := c.send("use", "template", args...)
	if err != nil {
		return "", err
	}
	if resp.Status == protocol.StatusError {
		return "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", fmt.Errorf("parsing result: %w", err)
	}
	return result.SessionID, nil
}

func (c *client) commitSession(sessionID, message string) error {
	resp, err := c.send("commit", "session", sessionID, "-m", message)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) pushSession(sessionID string) error {
	resp, err := c.send("push", "session", sessionID)
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		return fmt.Errorf("%s", resp.Message)
	}
	return nil
}

func (c *client) gitLog(sessionID string) (string, error) {
	resp, err := c.send("git-log", "session", sessionID)
	if err != nil {
		return "", err
	}
	if resp.Status == protocol.StatusError {
		return "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", err
	}
	return result.Message, nil
}

func (c *client) gitInfo(sessionID string) (string, error) {
	resp, err := c.send("git-info", "session", sessionID)
	if err != nil {
		return "", err
	}
	if resp.Status == protocol.StatusError {
		return "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", err
	}
	return result.Branch, nil
}
