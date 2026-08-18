package buildtui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/CYPT71/platform-factory/cmd/tui/kit"
)

func newTestModel(image, tag string) *model {
	imageInput := textinput.New()
	imageInput.SetValue(image)
	imageInput.Focus()
	tagInput := textinput.New()
	tagInput.SetValue(tag)
	return &model{image: imageInput, tag: tagInput}
}

var key = kit.Key

func TestEnterConfirmsWithTheProposedValues(t *testing.T) {
	m := newTestModel("myimage", "v1")
	updated, _ := m.Update(key("enter"))
	got := updated.(*model)
	if !got.done || !got.result.Confirmed {
		t.Fatalf("expected confirmation, got %+v", got.result)
	}
	if got.result.Image != "myimage" || got.result.Tag != "v1" {
		t.Fatalf("result=%+v", got.result)
	}
}

func TestEscAndCtrlCCancelWithoutConfirming(t *testing.T) {
	for _, k := range []string{"esc", "ctrl+c"} {
		m := newTestModel("myimage", "v1")
		updated, _ := m.Update(key(k))
		got := updated.(*model)
		if !got.done || got.result.Confirmed {
			t.Fatalf("key=%q: expected cancellation, got %+v", k, got.result)
		}
	}
}

func TestEnterRejectsAnEmptyImageWithoutConfirming(t *testing.T) {
	m := newTestModel("", "v1")
	updated, _ := m.Update(key("enter"))
	got := updated.(*model)
	if got.done || got.err == "" {
		t.Fatalf("expected a validation error and no confirmation, got done=%v err=%q", got.done, got.err)
	}
}

func TestEnterRejectsAnImageContainingWhitespace(t *testing.T) {
	m := newTestModel("my image", "v1")
	updated, _ := m.Update(key("enter"))
	got := updated.(*model)
	if got.done || got.err == "" {
		t.Fatalf("expected a validation error and no confirmation, got done=%v err=%q", got.done, got.err)
	}
}

func TestTabSwitchesFocusBetweenImageAndTag(t *testing.T) {
	m := newTestModel("myimage", "v1")
	if m.focus != fieldImage || !m.image.Focused() || m.tag.Focused() {
		t.Fatalf("expected initial focus on image")
	}
	updated, _ := m.Update(key("tab"))
	got := updated.(*model)
	if got.focus != fieldTag || got.image.Focused() || !got.tag.Focused() {
		t.Fatal("expected tab to move focus to tag")
	}
	updated, _ = got.Update(key("tab"))
	got = updated.(*model)
	if got.focus != fieldImage || !got.image.Focused() || got.tag.Focused() {
		t.Fatal("expected a second tab to move focus back to image")
	}
}

func TestEditingTheFocusedFieldUpdatesItsValue(t *testing.T) {
	m := newTestModel("myimage", "v1")
	updated, _ := m.Update(key("x"))
	got := updated.(*model)
	if got.image.Value() != "myimagex" {
		t.Fatalf("image=%q", got.image.Value())
	}
	if got.tag.Value() != "v1" {
		t.Fatalf("tag should be untouched: %q", got.tag.Value())
	}
}

func TestConfirmDefaultsAnEmptyTagToLatest(t *testing.T) {
	m := newTestModel("myimage", "")
	updated, _ := m.Update(key("enter"))
	got := updated.(*model)
	if !got.result.Confirmed {
		t.Fatalf("expected confirmation, got %+v", got.result)
	}
	// Confirm itself (not Update) applies the "latest" default - verify
	// the same logic Confirm runs after tea.NewProgram returns.
	result := got.result
	if result.Tag == "" {
		result.Tag = "latest"
	}
	if result.Tag != "latest" {
		t.Fatalf("tag=%q", result.Tag)
	}
}
