package handler

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"james/moneypenny/pkg/agent"
)

func agentFailureMessage(params agent.RunParams, err error) string {
	title := "Agent failed"
	action := "Retry the request. If it fails again, check the agent's configuration and daemon diagnostics."
	if errors.Is(err, errMemoryPreparation) {
		title = "Agent failed: memory preparation"
		action = "Check the session's memory store before retrying; the agent could not prepare its instructions."
	}

	var failure *agent.OpenCodeFailure
	if errors.As(err, &failure) {
		switch failure.Category {
		case "authentication":
			title = "Agent failed: authentication"
			action = "Check this provider's credentials or select another configured provider/model before retrying."
		case "rate_limit":
			title = "Agent failed: rate limit"
			action = "Wait before retrying, or select another available provider/model."
		case "context_limit":
			title = "Agent failed: context limit"
			action = "Compact the session or select a model with a larger context window before retrying."
		case "provider_unavailable":
			title = "Agent failed: provider unavailable"
			action = "Retry later or select another available provider/model."
		case "network":
			title = "Agent failed: network error"
			action = "Check connectivity to the provider before retrying."
		case "model_unavailable":
			title = "Agent failed: model unavailable"
			action = "Select an available model using its full provider/model ID before retrying."
		case "session_not_found":
			title = "Agent failed: agent session unavailable"
			action = "The underlying agent session could not be resumed. Check session recovery before retrying."
		}
		if failure.StatusCode >= 100 && failure.StatusCode <= 599 {
			title += fmt.Sprintf(" (HTTP %d)", failure.StatusCode)
		}
	}

	details := []string{title}
	if params.Agent != "" {
		details = append(details, "Agent: "+params.Agent)
	}
	if params.Model != "" {
		details = append(details, "Model: "+params.Model)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			details = append(details, fmt.Sprintf("Process exit code: %d", code))
		} else {
			details = append(details, "Process terminated without an exit code.")
		}
	}
	details = append(details, action)
	return strings.Join(details, "\n")
}
