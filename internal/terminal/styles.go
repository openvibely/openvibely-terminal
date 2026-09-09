package terminal

import "github.com/charmbracelet/lipgloss"

var (
	colorPrimary = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	colorOK      = lipgloss.AdaptiveColor{Light: "#059669", Dark: "#34D399"}
	colorErr     = lipgloss.AdaptiveColor{Light: "#DC2626", Dark: "#F87171"}
	colorDim     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#D97706", Dark: "#FBBF24"}
	colorAccent  = lipgloss.AdaptiveColor{Light: "#0891B2", Dark: "#22D3EE"}

	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)

	screenTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDim)

	// Slash-command bar
	paletteStyle    = lipgloss.NewStyle().Foreground(colorPrimary).Padding(0, 1)
	paletteSelStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)

	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)

	// Table header row: underlined so columns read as a table, not as data.
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDim).Underline(true)

	// Badges shown on task cards (Goal, Chain, Swarm, model, tag…).
	badgeStyle = lipgloss.NewStyle().Foreground(colorAccent)

	statusOKStyle  = lipgloss.NewStyle().Foreground(colorOK)
	statusErrStyle = lipgloss.NewStyle().Foreground(colorErr)
	dimStyle       = lipgloss.NewStyle().Foreground(colorDim)
	noticeStyle    = lipgloss.NewStyle().Foreground(colorWarn)

	helpStyle = lipgloss.NewStyle().Foreground(colorDim).Padding(0, 1)

	chatUserStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	chatAgentStyle = lipgloss.NewStyle().Bold(true).Foreground(colorOK)
)
