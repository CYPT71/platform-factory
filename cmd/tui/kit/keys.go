package kit

import tea "github.com/charmbracelet/bubbletea"

// IsQuitKey reports whether msg is Ctrl+C - the one keypress every
// prompt in this repo treats as an unconditional quit regardless of its
// own state.
func IsQuitKey(msg tea.Msg) bool {
	key, ok := msg.(tea.KeyMsg)
	return ok && key.String() == "ctrl+c"
}

// IsCancelKey reports whether msg is Ctrl+C or Esc - the "abandon and
// return an unconfirmed result" gesture a single-screen modal prompt
// treats identically. Do not use this where Esc has its own contextual
// meaning (e.g. going back a screen rather than quitting) - use
// IsQuitKey there instead and handle Esc separately.
func IsCancelKey(msg tea.Msg) bool {
	key, ok := msg.(tea.KeyMsg)
	return ok && (key.String() == "ctrl+c" || key.String() == "esc")
}
