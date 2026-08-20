package kit

import "testing"

func TestRenderBoxWrapsTheBodyInTheStandardBorder(t *testing.T) {
	got := RenderBox("hello")
	if got == "" || got == "hello" {
		t.Fatalf("expected RenderBox to wrap the body, got %q", got)
	}
}
