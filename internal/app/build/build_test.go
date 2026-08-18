package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseByteLimit(t *testing.T) {
	for _, args := range []string{"12MB", "-1"} {
		if _, err := ParseByteLimit(args); err == nil {
			t.Fatalf("expected %q to be rejected", args)
		}
	}
	for input, want := range map[string]int64{"0": 0, "512MiB": 512 << 20, "2GiB": 2 << 30, "4096": 4096} {
		got, err := ParseByteLimit(input)
		if err != nil || got != want {
			t.Fatalf("ParseByteLimit(%q)=%d,%v want=%d", input, got, err, want)
		}
	}
}

func TestTargetsRejectsAmbiguousSyntax(t *testing.T) {
	for _, test := range []struct {
		platforms, positional []string
	}{
		{nil, nil},
		{[]string{"bad"}, []string{"app"}},
		{[]string{"linux/amd64=app"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64"}, nil},
		{[]string{"linux/amd64=app", "linux/arm64=other"}, []string{"extra"}},
	} {
		if _, _, err := Targets(test.platforms, test.positional, "linux", "amd64"); err == nil {
			t.Fatalf("accepted platforms=%v positional=%v", test.platforms, test.positional)
		}
	}
}

// TestTargetsAndResolveTargetRemainingErrorBranches closes the few
// Targets/ResolveTarget error paths not already exercised through
// TestTargetsRejectsAmbiguousSyntax: a single --platform without exactly
// one executable, an invalid platform inside multi-platform syntax, and
// a non-executable input (detected as a different, unambiguous kind)
// reaching ResolveTarget.
func TestTargetsAndResolveTargetRemainingErrorBranches(t *testing.T) {
	if _, code, err := Targets([]string{"linux/amd64"}, nil, "linux", "amd64"); err == nil || code != 2 {
		t.Fatalf("single platform without executable: code=%d err=%v", code, err)
	}
	if _, code, err := Targets(
		[]string{"bogus/arch=a", "linux/amd64=b"}, nil, "linux", "amd64",
	); err == nil || code != 2 {
		t.Fatalf("invalid platform in multi-platform syntax: code=%d err=%v", code, err)
	}
	root := t.TempDir()
	script := filepath.Join(root, "app.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveTarget(Target{OS: "linux", Architecture: "amd64", Input: script}, Settings{}); err == nil {
		t.Fatal("expected a non-ELF, non-unknown input to be rejected")
	}
}
