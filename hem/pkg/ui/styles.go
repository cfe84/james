package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Colors
	colorPrimary   = lipgloss.Color("#F97316") // orange
	colorSecondary = lipgloss.Color("#6B7280") // gray
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorDanger    = lipgloss.Color("#EF4444") // red
	colorMuted     = lipgloss.Color("#9CA3AF")
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
	sessionSelectedBg    = lipgloss.Color("#4B5563")
	sessionSelectedStyle = lipgloss.NewStyle().
				Background(sessionSelectedBg).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)

	sessionNormalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#D1D5DB"))

	sessionHeaderStyle = lipgloss.NewStyle().
				Foreground(colorSecondary).
				Bold(true)

	statusReady   = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Render("● ready")
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
			Width(24)

	fieldActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(colorBgLight).
				Padding(0, 1)

	fieldInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#9CA3AF")).
				Padding(0, 1)
)

var statusOffline = lipgloss.NewStyle().Foreground(colorDanger).Render("✗ offline")

// statusPlain returns a plain-text status string (no ANSI), for use in selected rows.
func statusPlain(status string) string {
	switch status {
	case "ready":
		return "● ready"
	case "idle":
		return "● idle"
	case "working":
		return "◉ working"
	case "offline":
		return "✗ offline"
	default:
		return "? " + status
	}
}

func statusBadge(status string) string {
	switch status {
	case "ready":
		return statusReady
	case "idle":
		return statusIdle
	case "working":
		return statusWorking
	case "offline":
		return statusOffline
	default:
		return lipgloss.NewStyle().Foreground(colorDanger).Render("? " + status)
	}
}

// padRight pads a (possibly styled) string to a fixed visual width.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
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

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
