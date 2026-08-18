package kit

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// quitModel is a trivial tea.Model that quits itself on Init, so tests
// can run a real tea.Program without touching an actual terminal or
// blocking on input.
type quitModel struct{ value int }

func (m quitModel) Init() tea.Cmd                       { return tea.Quit }
func (m quitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m quitModel) View() string                        { return "" }

func testOpts() []tea.ProgramOption {
	return []tea.ProgramOption{tea.WithInput(strings.NewReader("")), tea.WithoutRenderer()}
}

func TestLaunchExtractsTheFinalModel(t *testing.T) {
	got, err := Launch(quitModel{value: 42}, func(m quitModel) int { return m.value }, testOpts()...)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestLaunchVoidReturnsNoErrorWhenTheProgramQuitsCleanly(t *testing.T) {
	if err := LaunchVoid(quitModel{}, testOpts()...); err != nil {
		t.Fatalf("LaunchVoid: %v", err)
	}
}
