package kit

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestIsQuitKey(t *testing.T) {
	cases := []struct {
		msg  tea.Msg
		want bool
	}{
		{tea.KeyMsg{Type: tea.KeyCtrlC}, true},
		{tea.KeyMsg{Type: tea.KeyEsc}, false},
		{tea.KeyMsg{Type: tea.KeyEnter}, false},
		{tea.WindowSizeMsg{}, false},
	}
	for _, c := range cases {
		if got := IsQuitKey(c.msg); got != c.want {
			t.Errorf("IsQuitKey(%#v) = %v, want %v", c.msg, got, c.want)
		}
	}
}

func TestIsCancelKey(t *testing.T) {
	cases := []struct {
		msg  tea.Msg
		want bool
	}{
		{tea.KeyMsg{Type: tea.KeyCtrlC}, true},
		{tea.KeyMsg{Type: tea.KeyEsc}, true},
		{tea.KeyMsg{Type: tea.KeyEnter}, false},
		{tea.WindowSizeMsg{}, false},
	}
	for _, c := range cases {
		if got := IsCancelKey(c.msg); got != c.want {
			t.Errorf("IsCancelKey(%#v) = %v, want %v", c.msg, got, c.want)
		}
	}
}
