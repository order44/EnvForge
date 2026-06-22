package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Color palette
	colorPrimary   = lipgloss.Color("#7C3AED") // purple
	colorSecondary = lipgloss.Color("#06B6D4") // cyan
	colorSuccess   = lipgloss.Color("#10B981") // green
	colorError     = lipgloss.Color("#EF4444") // red
	colorWarning   = lipgloss.Color("#F59E0B") // yellow
	colorMuted     = lipgloss.Color("#9CA3AF") // lighter gray (was too dark before)
	colorMutedDim  = lipgloss.Color("#6B7280") // for very subtle text
	colorFg        = lipgloss.Color("#F3F4F6") // near-white
	colorAccent    = lipgloss.Color("#818CF8") // indigo

	// Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			PaddingLeft(1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			PaddingLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorFg).
			Background(colorPrimary).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	unselectedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	cursorStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	categoryStyle = lipgloss.NewStyle().
			Foreground(colorFg)

	packageStyle = lipgloss.NewStyle().
			Foreground(colorFg).
			PaddingLeft(2)

	descStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	dimStyle = lipgloss.NewStyle().
			Foreground(colorMutedDim)

	statusOKStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	statusFailStyle = lipgloss.NewStyle().
			Foreground(colorError)

	statusRunStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	statusSkipStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			PaddingLeft(1).
			PaddingTop(1)

	progressBarFilled = lipgloss.NewStyle().
				Foreground(colorSuccess)

	progressBarEmpty = lipgloss.NewStyle().
				Foreground(colorMutedDim)

	counterStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	streamLineStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			PaddingLeft(4)

	streamErrStyle = lipgloss.NewStyle().
			Foreground(colorWarning).
			PaddingLeft(4)
)
