package runtimetui

import (
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

func TestNoHostCandidateOmitsTheHostChoice(t *testing.T) {
	m := newTestModel("")
	for _, c := range m.choices {
		if c.source == SourceHost {
			t.Fatal("expected no host choice when hostCandidate is empty")
		}
	}
}
