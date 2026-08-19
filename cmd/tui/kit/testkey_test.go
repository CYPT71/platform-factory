package kit

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestKey(t *testing.T) {
	cases := []struct {
		in   string
		want tea.KeyMsg
	}{
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}},
		{"shift+tab", tea.KeyMsg{Type: tea.KeyShiftTab}},
		{"up", tea.KeyMsg{Type: tea.KeyUp}},
		{"down", tea.KeyMsg{Type: tea.KeyDown}},
		{"x", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}},
		{"hello", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")}},
	}
	for _, c := range cases {
		got := Key(c.in)
		if got.Type != c.want.Type || got.String() != c.want.String() {
			t.Errorf("Key(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
