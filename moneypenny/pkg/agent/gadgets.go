package agent

// These grants expose only the thin client. The daemon remains the authority
// for identity and capabilities, including when an agent runs in yolo mode.
func gadgetAccessArgs(agentName string, params RunParams) []string {
	if params.Environment["JAMES_GADGETS_TOKEN"] == "" || params.Yolo {
		return nil
	}
	switch agentName {
	case "claude":
		return []string{"--allowedTools", "Bash(gadgets:*)"}
	case "copilot":
		return []string{"--allow-tool", "shell(gadgets:*)"}
	}
	return nil
}
