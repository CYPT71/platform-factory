// Package kit provides the shared bubbletea styling, program-launch, and
// key-handling primitives used by every terminal prompt under cmd/tui -
// buildtui, runtimetui, marketplacetui - so each one no longer redeclares
// the same lipgloss styles and tea.Program boilerplate by hand.
package kit

import "github.com/charmbracelet/lipgloss"

var (
	Accent = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	Muted  = lipgloss.AdaptiveColor{Light: "243", Dark: "245"}

	TitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(Accent)
	DimStyle     = lipgloss.NewStyle().Foreground(Muted)
	ErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	SuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})
	FocusStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "127", Dark: "212"})
	// CursorStyle is an alias of FocusStyle: marketplacetui declares it
	// under its own name for a version-list cursor, buildtui/runtimetui
	// use the same look for field/choice focus - one style, two names
	// callers reach for depending on context.
	CursorStyle = FocusStyle
	// HelpStyle is an alias of DimStyle: every prompt's footer help line
	// uses the same look as its other secondary text.
	HelpStyle = DimStyle
	BoxStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Muted).Padding(0, 1)
)

// RenderBox wraps body in the standard rounded-border single-screen box
// a modal prompt's View() returns.
func RenderBox(body string) string { return BoxStyle.Render(body) }
