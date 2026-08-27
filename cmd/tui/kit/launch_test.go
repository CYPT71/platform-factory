package kit

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// waitModel never quits on its own, so a program built around it stays
// alive long enough to actually read from (and error out on) a failing
// input reader, unlike quitModel which may race the read with its own
// self-issued tea.Quit.
type waitModel struct{ value int }

func (m waitModel) Init() tea.Cmd                       { return nil }
func (m waitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m waitModel) View() string                        { return "" }

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

func TestLaunchReturnsTheZeroValueOnAProgramError(t *testing.T) {
	got, err := Launch(waitModel{value: 42}, func(m waitModel) int { return m.value },
		tea.WithInput(failingReader{}), tea.WithoutRenderer())
	if err == nil {
		t.Fatal("expected Launch to surface the program's error")
	}
	if got != 0 {
		t.Fatalf("got %d, want the zero value on error", got)
	}
}
