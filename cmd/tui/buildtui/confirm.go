// Package buildtui is a small terminal confirmation prompt for the OCI
// image reference (name:tag) a build is about to embed as its
// org.opencontainers.image.ref.name annotation (see
// internal/oci/builder.go). It is shown only on a real interactive
// terminal - callers gate it on isatty themselves - and lets the person
// running the build see and, if they want, edit the proposed reference
// before it's baked into the layout.
package buildtui

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

// Result is what Confirm returns. Confirmed is false when the user
// canceled (Esc/Ctrl+C) - a caller must not proceed with a build in
// that case, the same way it wouldn't after any other user-refused
// confirmation.
type Result struct {
	Image     string
	Tag       string
	Confirmed bool
}

type field int

const (
	fieldImage field = iota
	fieldTag
)

type model struct {
	image, tag textinput.Model
	focus      field
	err        string
	result     Result
	done       bool
}

// Confirm opens a one-screen prompt on the process terminal, pre-filled
// with proposedImage/proposedTag, and returns what the user confirmed.
// Tab/Shift+Tab (or Up/Down) switches between the two fields, Enter
// confirms (rejected with an inline error if the image field is empty
// or contains whitespace - the same shape validation the CLI's own
// --image/--tag flags rely on), Esc/Ctrl+C cancels.
func Confirm(proposedImage, proposedTag string) (Result, error) {
	image := textinput.New()
	image.Prompt = ""
	image.SetValue(proposedImage)
	image.CharLimit = 255
	image.Width = 40
	image.Focus()

	tag := textinput.New()
	tag.Prompt = ""
	tag.SetValue(proposedTag)
	tag.CharLimit = 128
	tag.Width = 40

	m := &model{image: image, tag: tag}
	finalModel, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	result := finalModel.(*model).result
	if result.Confirmed && result.Tag == "" {
		result.Tag = "latest"
	}
	return result, nil
}

func (m *model) Init() tea.Cmd { return textinput.Blink }

func validReference(image string) bool {
	trimmed := strings.TrimSpace(image)
	return trimmed != "" && trimmed == image && !strings.ContainsAny(image, " \t\r\n\x00")
}

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := message.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.result, m.done = Result{Confirmed: false}, true
			return m, tea.Quit
		case "enter":
			if !validReference(m.image.Value()) {
				m.err = "image must be non-empty with no surrounding or embedded whitespace"
				return m, nil
			}
			m.err = ""
			m.result = Result{Image: m.image.Value(), Tag: strings.TrimSpace(m.tag.Value()), Confirmed: true}
			m.done = true
			return m, tea.Quit
		case "tab", "shift+tab", "up", "down":
			m.err = ""
			if m.focus == fieldImage {
				m.focus, m.image, m.tag = fieldTag, blur(m.image), focusInput(m.tag)
			} else {
				m.focus, m.tag, m.image = fieldImage, blur(m.tag), focusInput(m.image)
			}
			return m, nil
		}
	}
	var commands [2]tea.Cmd
	m.image, commands[0] = m.image.Update(message)
	m.tag, commands[1] = m.tag.Update(message)
	return m, tea.Batch(commands[0], commands[1])
}

func focusInput(input textinput.Model) textinput.Model {
	input.Focus()
	return input
}

func blur(input textinput.Model) textinput.Model {
	input.Blur()
	return input
}

func (m *model) View() string {
	if m.done {
		return ""
	}
	var b strings.Builder
	fmt.Fprintln(&b, titleStyle.Render("Confirm image reference"))
	fmt.Fprintln(&b, dimStyle.Render("Embedded as org.opencontainers.image.ref.name."))
	b.WriteString("\n")
	imageLabel, tagLabel := "Image", "Tag  "
	if m.focus == fieldImage {
		imageLabel = focusStyle.Render("Image")
	} else {
		tagLabel = focusStyle.Render("Tag  ")
	}
	fmt.Fprintf(&b, "%s  %s\n", imageLabel, m.image.View())
	fmt.Fprintf(&b, "%s  %s\n", tagLabel, m.tag.View())
	b.WriteString("\n")
	fmt.Fprintf(&b, "-> %s\n", dimStyle.Render(m.image.Value()+":"+m.tag.Value()))
	if m.err != "" {
		fmt.Fprintln(&b, errorStyle.Render(m.err))
	}
	fmt.Fprint(&b, helpStyle.Render("tab switch field  enter confirm  esc cancel"))
	return boxStyle.Render(b.String())
}
