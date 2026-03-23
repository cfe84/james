package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"james/hem/pkg/store"
)

// sessionParams holds the parameters for creating or continuing a session.
type sessionParams struct {
	MoneypennyName string
	SessionName    string
	SystemPrompt   string
	Path           string
	Agent          string
	Model          string
	Effort         string
	ProjectID      string
	Yolo           bool
	Gadgets        bool
	Async          bool
}

// resolveMoneypennyForSession resolves the moneypenny to use for a session.
// If mpName is provided, it looks it up. Otherwise, it returns the default moneypenny.
func (e *Executor) resolveMoneypennyForSession(mpName string) (*store.Moneypenny, error) {
	if mpName != "" {
		mp, err := e.store.GetMoneypenny(mpName)
		if err != nil {
			return nil, err
		}
		if mp == nil {
			return nil, fmt.Errorf("moneypenny %q not found", mpName)
		}
		return mp, nil
	}

	mp, err := e.store.GetDefaultMoneypenny()
	if err != nil {
		return nil, err
	}
	if mp == nil {
		return nil, fmt.Errorf("no moneypenny specified and no default set")
	}
	return mp, nil
}

// applyProjectDefaults applies project defaults to session parameters if they're not already set.
func (e *Executor) applyProjectDefaults(params *sessionParams, projectNameOrID string) error {
	if projectNameOrID == "" {
		return nil
	}

	proj, err := e.store.GetProject(projectNameOrID)
	if err != nil {
		return err
	}
	if proj == nil {
		return fmt.Errorf("project %q not found", projectNameOrID)
	}

	params.ProjectID = proj.ID

	if params.MoneypennyName == "" && proj.Moneypenny != "" {
		params.MoneypennyName = proj.Moneypenny
	}
	if params.Agent == "" && proj.DefaultAgent != "" {
		params.Agent = proj.DefaultAgent
	}
	if params.Path == "" && proj.Paths != "[]" && proj.Paths != "" {
		// Use the first path from the JSON array.
		var paths []string
		if json.Unmarshal([]byte(proj.Paths), &paths) == nil && len(paths) > 0 {
			params.Path = paths[0]
		}
	}
	if params.SystemPrompt == "" && proj.DefaultSystemPrompt != "" {
		params.SystemPrompt = proj.DefaultSystemPrompt
	}

	return nil
}

// applyGlobalDefaults applies global defaults for agent and path if not specified.
func (e *Executor) applyGlobalDefaults(params *sessionParams) {
	if params.Agent == "" {
		if v, _ := e.store.GetDefault("agent"); v != "" {
			params.Agent = v
		} else {
			params.Agent = "claude"
		}
	}
	if params.Path == "" {
		if v, _ := e.store.GetDefault("path"); v != "" {
			params.Path = v
		} else {
			params.Path = "."
		}
	}
}

// generateSessionName generates a session name from the prompt if none is provided.
func generateSessionName(prompt, providedName string) string {
	if providedName != "" {
		return providedName
	}
	name := prompt
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// buildCreateSessionData builds the command data map for create_session.
func buildCreateSessionData(params *sessionParams, sessionID, prompt string) map[string]interface{} {
	cmdData := map[string]interface{}{
		"agent":      params.Agent,
		"session_id": sessionID,
		"name":       params.SessionName,
		"prompt":     prompt,
		"path":       params.Path,
	}
	if params.SystemPrompt != "" {
		cmdData["system_prompt"] = params.SystemPrompt
	}
	if params.Model != "" {
		cmdData["model"] = params.Model
	}
	if params.Effort != "" {
		cmdData["effort"] = params.Effort
	}
	if params.Yolo {
		cmdData["yolo"] = true
	}
	return cmdData
}

// validatePrompt validates that a prompt is provided and non-empty.
func validatePrompt(remaining []string) (string, error) {
	prompt := strings.TrimSpace(strings.Join(remaining, " "))
	if prompt == "" {
		return "", fmt.Errorf("prompt is required (pass as trailing arguments)")
	}
	return prompt, nil
}
