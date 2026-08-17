// Package runtimetui is a small terminal prompt for one decision:
// where should pf init provision a language runtime (the interpreter
// and its shared libraries, staged into pf.yaml's include list) from -
// a matching interpreter already on this host, or a pulled base image?
// Provisioning itself (finding/copying a host interpreter's runtime
// closure, or pulling and extracting an image) is the caller's job;
// this package only asks the question, on a real interactive terminal.
package runtimetui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	accent     = lipgloss.AdaptiveColor{Light: "25", Dark: "39"}
	muted      = lipgloss.AdaptiveColor{Light: "243", Dark: "245"}
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle   = lipgloss.NewStyle().Foreground(muted)
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	focusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "127", Dark: "212"})
	helpStyle  = dimStyle
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(muted).Padding(0, 1)
)

// Source is which of the two provisioning options the user picked.
type Source int

const (
	// SourceSkip means the user canceled - the caller leaves pf.yaml's
	// runtime field unset, exactly like today's "run `pf plugin
	// provision-runtime` later" fallback.
	SourceSkip Source = iota
	SourceHost
	SourceImage
)

// Result is what Confirm returns.
type Result struct {
	Source Source
	// Image is the reference to pull, set only when Source is
	// SourceImage.
	Image string
}

type choice struct {
	source Source
	label  string
}

type model struct {
	language      string
	hostCandidate string
	hostArch      string
	choices       []choice
	cursor        int
	imageInput    textinput.Model
	editingImage  bool
	err           string
	result        Result
	done          bool
}

// Confirm asks how to provision language's runtime. hostCandidate is the
// path to a matching interpreter already found on this host (empty if
// none was found - only "pull an image" and "skip" are then offered).
func Confirm(language, hostCandidate, hostArch string) (Result, error) {
	choices := []choice{}
	if hostCandidate != "" {
		choices = append(choices, choice{source: SourceHost, label: fmt.Sprintf("Use the %s interpreter already on this host (%s, %s)", language, hostCandidate, hostArch)})
	}
	choices = append(choices,
		choice{source: SourceImage, label: "Pull a base image and extract the runtime from it"},
		choice{source: SourceSkip, label: "Skip for now (run `pf plugin provision-runtime` later)"},
	)
	image := textinput.New()
	image.Prompt = ""
	image.Placeholder = "e.g. " + language + "@sha256:..."
	image.CharLimit = 255
	image.Width = 50

	m := &model{language: language, hostCandidate: hostCandidate, hostArch: hostArch, choices: choices, imageInput: image}
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	return finalModel.(*model).result, nil
}

func (m *model) Init() tea.Cmd { return textinput.Blink }

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := message.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.editingImage {
		switch key.String() {
		case "ctrl+c", "esc":
			m.editingImage = false
			return m, nil
		case "enter":
			ref := strings.TrimSpace(m.imageInput.Value())
			if ref == "" {
				m.err = "image reference must not be empty"
				return m, nil
			}
			m.err = ""
			m.result, m.done = Result{Source: SourceImage, Image: ref}, true
			return m, tea.Quit
		}
		var command tea.Cmd
		m.imageInput, command = m.imageInput.Update(message)
		return m, command
	}
	switch key.String() {
	case "ctrl+c", "esc":
		m.result, m.done = Result{Source: SourceSkip}, true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		selected := m.choices[m.cursor]
		if selected.source == SourceImage {
			m.editingImage = true
			m.imageInput.Focus()
			return m, textinput.Blink
		}
		m.result, m.done = Result{Source: selected.source}, true
		return m, tea.Quit
	}
	return m, nil
}

func (m *model) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	fmt.Fprintln(&b, titleStyle.Render("Provision the "+m.language+" runtime"))
	fmt.Fprintln(&b, dimStyle.Render("Staged into pf.yaml's include list, never fetched again during a build."))
	b.WriteString("\n")
	if m.editingImage {
		fmt.Fprintln(&b, "Image reference:")
		fmt.Fprintln(&b, m.imageInput.View())
		if m.err != "" {
			fmt.Fprintln(&b, errorStyle.Render(m.err))
		}
		fmt.Fprint(&b, helpStyle.Render("enter confirm  esc back"))
		return boxStyle.Render(b.String())
	}
	for index, option := range m.choices {
		line := option.label
		if index == m.cursor {
			line = focusStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		fmt.Fprintln(&b, line)
	}
	fmt.Fprint(&b, helpStyle.Render("up/down select  enter confirm  esc skip"))
	return boxStyle.Render(b.String())
}
