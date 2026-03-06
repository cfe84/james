package ui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#6B7280") // gray
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorDanger    = lipgloss.Color("#EF4444") // red
	colorMuted     = lipgloss.Color("#4B5563")
	colorBg        = lipgloss.Color("#1F2937")
	colorBgLight   = lipgloss.Color("#374151")

	// Status bar
	statusBarStyle = lipgloss.NewStyle().
			Background(colorPrimary).
			Foreground(lipgloss.Color("#FFFFFF")).
			Padding(0, 1)

	statusKeyStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#FFFFFF")).
			Foreground(colorPrimary).
			Padding(0, 1).
			Bold(true)

	statusDescStyle = lipgloss.NewStyle().
			Background(colorPrimary).
			Foreground(lipgloss.Color("#E5E7EB")).
			Padding(0, 1)

	// Session list
	sessionSelectedStyle = lipgloss.NewStyle().
				Background(colorBgLight).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)

	sessionNormalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#D1D5DB"))

	sessionHeaderStyle = lipgloss.NewStyle().
				Foreground(colorSecondary).
				Bold(true)

	statusIdle    = lipgloss.NewStyle().Foreground(colorSuccess).Render("● idle")
	statusWorking = lipgloss.NewStyle().Foreground(colorWarning).Render("◉ working")

	// Chat
	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#60A5FA")).
			Bold(true)

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	msgContentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))

	inputPromptStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	// Title
	titleStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true).
			Padding(0, 1)

	// Borders
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted)

	// Dialog
	dialogStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(1, 2)

	labelStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Width(16)

	fieldActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(colorBgLight).
				Padding(0, 1)

	fieldInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Padding(0, 1)
)

func statusBadge(status string) string {
	switch status {
	case "idle":
		return statusIdle
	case "working":
		return statusWorking
	default:
		return lipgloss.NewStyle().Foreground(colorDanger).Render("? " + status)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
