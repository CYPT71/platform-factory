package kit

import tea "github.com/charmbracelet/bubbletea"

// Key returns the tea.KeyMsg a real terminal sends for a named key
// (enter, esc, ctrl+c, tab, shift+tab, up, down) or, for any other
// string, the KeyMsg for typing that string's runes - the mapping every
// TUI package's tests previously hand-declared as their own unexported
// key(s string) helper.
func Key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}
