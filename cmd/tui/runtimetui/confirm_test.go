package runtimetui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/CYPT71/platform-factory/cmd/tui/kit"
)

func newTestModel(hostCandidate string) *model {
	choices := []choice{}
	if hostCandidate != "" {
		choices = append(choices, choice{source: SourceHost, label: "host"})
	}
	choices = append(choices, choice{source: SourceImage, label: "image"}, choice{source: SourceSkip, label: "skip"})
	return &model{language: "python", hostCandidate: hostCandidate, choices: choices, imageInput: textinput.New()}
}

var key = kit.Key

func TestEnterOnTheFirstChoiceSelectsHostWhenOffered(t *testing.T) {
	m := newTestModel("/usr/bin/python3")
	updated, _ := m.Update(key("enter"))
	got := updated.(*model)
	if !got.done || got.result.Source != SourceHost {
		t.Fatalf("result=%+v", got.result)
	}
}

func TestDownThenEnterSelectsImageAndPromptsForAReference(t *testing.T) {
	m := newTestModel("/usr/bin/python3")
	updated, _ := m.Update(key("down"))
	got := updated.(*model)
	updated, _ = got.Update(key("enter"))
	got = updated.(*model)
	if got.done || !got.editingImage {
		t.Fatalf("expected the image reference prompt to open, got done=%v editingImage=%v", got.done, got.editingImage)
	}
	updated, _ = got.Update(key("p"))
	got = updated.(*model)
	updated, _ = got.Update(key("enter"))
	got = updated.(*model)
	if !got.done || got.result.Source != SourceImage || got.result.Image != "p" {
		t.Fatalf("result=%+v", got.result)
	}
}

func TestEnterOnAnEmptyImageReferenceIsRejected(t *testing.T) {
	m := newTestModel("")
	updated, _ := m.Update(key("enter")) // first (only) choice without a host candidate is "image"
	got := updated.(*model)
	if !got.editingImage {
		t.Fatalf("expected the image reference prompt to open, got %+v", got)
	}
	updated, _ = got.Update(key("enter"))
	got = updated.(*model)
	if got.done || got.err == "" {
		t.Fatalf("expected a validation error and no confirmation, got done=%v err=%q", got.done, got.err)
	}
}

func TestEscCancelsWithSourceSkip(t *testing.T) {
	m := newTestModel("/usr/bin/python3")
	updated, _ := m.Update(key("esc"))
	got := updated.(*model)
	if !got.done || got.result.Source != SourceSkip {
		t.Fatalf("result=%+v", got.result)
	}
}

func TestInitReturnsTheTextInputBlinkCommand(t *testing.T) {
	m := newTestModel("/usr/bin/python3")
	if m.Init() == nil {
		t.Fatal("expected a non-nil Init command")
	}
}

func TestViewRendersChoicesThenTheImagePromptOnceEditing(t *testing.T) {
	m := newTestModel("/usr/bin/python3")
	view := m.View()
	if !strings.Contains(view, "Provision the python runtime") || !strings.Contains(view, "up/down select") {
		t.Fatalf("view=%q", view)
	}

	updated, _ := m.Update(key("down"))
	m = updated.(*model)
	updated, _ = m.Update(key("enter"))
	m = updated.(*model)
	if !m.editingImage {
		t.Fatal("expected the image prompt to be open")
	}
	view = m.View()
	if !strings.Contains(view, "Image reference:") || !strings.Contains(view, "esc back") {
		t.Fatalf("view=%q", view)
	}

	m.err = "boom"
	if !strings.Contains(m.View(), "boom") {
		t.Fatal("expected the error message to be rendered")
	}

	m.done = true
	if m.View() != "" {
		t.Fatalf("expected an empty view once done, got %q", m.View())
	}
}

func TestNoHostCandidateOmitsTheHostChoice(t *testing.T) {
	m := newTestModel("")
	for _, c := range m.choices {
		if c.source == SourceHost {
			t.Fatal("expected no host choice when hostCandidate is empty")
		}
	}
}
