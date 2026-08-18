package kit

import tea "github.com/charmbracelet/bubbletea"

// Launch runs a bubbletea program built around m to completion on the
// process terminal, then applies extract to the final, concretely-typed
// model to produce the typed result the caller actually wants -
// replacing the tea.NewProgram(m).Run() + type-assert boilerplate every
// existing prompt package repeated by hand.
func Launch[M tea.Model, R any](m M, extract func(M) R, opts ...tea.ProgramOption) (R, error) {
	finalModel, err := tea.NewProgram(m, opts...).Run()
	if err != nil {
		var zero R
		return zero, err
	}
	return extract(finalModel.(M)), nil
}

// LaunchVoid runs a bubbletea program to completion and returns only the
// error - for a program whose result is observed through side effects
// rather than a returned model field (e.g. marketplacetui.Run).
func LaunchVoid(m tea.Model, opts ...tea.ProgramOption) error {
	_, err := tea.NewProgram(m, opts...).Run()
	return err
}
