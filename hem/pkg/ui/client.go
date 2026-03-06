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
	sockPath string
}

func newClient() *client {
	return &client{sockPath: server.DefaultSocketPath()}
}

func (c *client) send(verb, noun string, args ...string) (*protocol.Response, error) {
	req := &protocol.Request{Verb: verb, Noun: noun, Args: args}
	return hemclient.Send(c.sockPath, req)
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
	Yolo         bool               `json:"yolo"`
	Path         string             `json:"path"`
	Status       string             `json:"status"`
	Conversation []conversationTurn `json:"conversation"`
}

type conversationTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
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

func (c *client) getHistory(sessionID string) ([]conversationTurn, error) {
	resp, err := c.send("history", "session", sessionID)
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s", resp.Message)
	}

	var result struct {
		Conversation []conversationTurn `json:"conversation"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("parsing history: %w", err)
	}
	return result.Conversation, nil
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

func (c *client) continueSession(sessionID, prompt string) (string, error) {
	resp, err := c.send("continue", "session", sessionID, prompt)
	if err != nil {
		return "", err
	}
	if resp.Status == protocol.StatusError {
		return "", fmt.Errorf("%s", resp.Message)
	}
	var result struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", err
	}
	return result.Response, nil
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
