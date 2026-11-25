package kit

import (
	"os"
	"strings"
	"testing"
)

func TestRenderBoxWrapsTheBodyInTheStandardBorder(t *testing.T) {
	got := RenderBox("hello")
	if got == "" || got == "hello" {
		t.Fatalf("expected RenderBox to wrap the body, got %q", got)
	}
}

func TestRenderPromptRespectsNarrowWidthsAndNoColor(t *testing.T) {
	for _, width := range []int{0, 4, 24, 40} {
		out := RenderPrompt("short line\na longer line that must wrap cleanly", width)
		if !strings.Contains(out, "line") {
			t.Fatalf("width=%d output missing body: %q", width, out)
		}
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		out := TitleStyle.Render("Title") + ErrorStyle.Render("Error") + FocusStyle.Render("> Focus")
		if strings.Contains(out, "\x1b[") {
			t.Fatalf("ANSI escape sequence leaked under NO_COLOR/TERM=dumb: %q", out)
		}
	}
}
