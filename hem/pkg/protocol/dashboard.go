package protocol

// DashboardResult is the structured dashboard projection. Sessions are in tree
// order within each workstream; execution, unread and completion are independent.
type DashboardResult struct {
	Workstreams []Workstream `json:"workstreams"`
}

type DashboardSession struct {
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Name            string `json:"name"`
	Nick            string `json:"nick,omitempty"`
	Project         string `json:"project,omitempty"`
	Moneypenny      string `json:"moneypenny"`
	Agent           string `json:"agent,omitempty"`
	Model           string `json:"model,omitempty"`
	Execution       string `json:"execution"`
	Unread          bool   `json:"unread"`
	Completed       bool   `json:"completed"`
	CreatedAt       string `json:"created_at,omitempty"`
	LastActivity    string `json:"last_activity,omitempty"`
	Depth           int    `json:"depth"`
	HierarchyIssue  string `json:"hierarchy_issue,omitempty"`
}

type Workstream struct {
	RootSessionID string             `json:"root_session_id"`
	Section       string             `json:"section"`
	Sessions      []DashboardSession `json:"sessions"`
	Running       int                `json:"running"`
	Unread        int                `json:"unread"`
	Unavailable   int                `json:"unavailable"`
	Active        int                `json:"active"`
	LastActivity  string             `json:"last_activity,omitempty"`
}

// UnsupportedDashboardView recognizes only the capability error from older Hem
// servers, not connectivity, storage or other dashboard failures.
func UnsupportedDashboardView(message string) bool {
	return message == "flag provided but not defined: -view"
}
