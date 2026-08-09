package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestParseComponents(t *testing.T) {
	cases := map[string][]string{
		"":                    nil,
		"builder":             {"builder"},
		"builder,microvm":     {"builder", "microvm"},
		" builder , microvm ": {"builder", "microvm"},
		"builder,,microvm":    {"builder", "microvm"},
	}
	for input, want := range cases {
		got := parseComponents(input)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseComponents(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestResolveComponentsAlwaysIncludesCore(t *testing.T) {
	selected, err := resolveComponents(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].key != "core" {
		t.Fatalf("selected=%+v", selected)
	}
}

func TestResolveComponentsDeduplicatesAndPreservesCatalogOrder(t *testing.T) {
	selected, err := resolveComponents([]string{"distributed", "core", "builder", "distributed"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range selected {
		keys = append(keys, c.key)
	}
	want := []string{"core", "builder", "distributed"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys=%v want=%v", keys, want)
	}
}

func TestResolveComponentsRejectsUnknownKey(t *testing.T) {
	if _, err := resolveComponents([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown component")
	}
}

func TestBuildStepsFlattensBinariesForEachSelectedComponent(t *testing.T) {
	selected, err := resolveComponents([]string{"microvm"})
	if err != nil {
		t.Fatal(err)
	}
	steps := buildSteps(selected, "linux", "amd64")
	var names []string
	for _, s := range steps {
		names = append(names, s.name)
		if s.pkg != "./cmd/"+s.name {
			t.Errorf("step %q pkg=%q", s.name, s.pkg)
		}
		if s.cgo {
			t.Errorf("step %q should not require cgo on linux", s.name)
		}
	}
	want := []string{"platform-factory", "microvm-init", "microvm-initramfs", "platform-factory-runtime"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
}

func TestBuildStepsEnablesCGOOnlyOnTheMatchingNativeMacHost(t *testing.T) {
	selected, err := resolveComponents(nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := buildSteps(selected, "darwin", "arm64")
	if len(steps) != 1 || steps[0].name != "platform-factory" {
		t.Fatalf("steps=%+v", steps)
	}
	wantCGO := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	if steps[0].cgo != wantCGO {
		t.Fatalf("cgo=%v want=%v (host=%s/%s)", steps[0].cgo, wantCGO, runtime.GOOS, runtime.GOARCH)
	}

	// Cross-building darwin/arm64 from a different host must never enable
	// cgo, regardless of what the host actually is.
	steps = buildSteps(selected, "darwin", "riscv64")
	if steps[0].cgo {
		t.Fatalf("cgo=true for an architecture no host can match natively")
	}
}

func TestBinSuffix(t *testing.T) {
	if got := binSuffix("windows"); got != ".exe" {
		t.Fatalf("binSuffix(windows)=%q", got)
	}
	if got := binSuffix("linux"); got != "" {
		t.Fatalf("binSuffix(linux)=%q", got)
	}
}

func TestEnsurePFAliasNoopWhenPlatformFactoryNotInstalled(t *testing.T) {
	prefix := t.TempDir()
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, "pf")); !os.IsNotExist(err) {
		t.Fatalf("pf alias created without platform-factory: err=%v", err)
	}
}

func TestEnsurePFAliasPointsAtPlatformFactory(t *testing.T) {
	prefix := t.TempDir()
	suffix := binSuffix(runtime.GOOS)
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"+suffix), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(prefix, "pf"+suffix)
	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(alias)
		if err != nil || string(data) != "binary" {
			t.Fatalf("pf alias content=%q err=%v, want a copy of platform-factory", data, err)
		}
		return
	}
	linkTarget, err := os.Readlink(alias)
	if err != nil {
		t.Fatalf("pf is not a symlink: %v", err)
	}
	if linkTarget != "platform-factory"+suffix {
		t.Fatalf("pf symlink target=%q, want a relative %q", linkTarget, "platform-factory"+suffix)
	}
}

func TestEnsurePFAliasReplacesAStaleAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows by default")
	}
	prefix := t.TempDir()
	if err := os.WriteFile(filepath.Join(prefix, "platform-factory"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(prefix, "pf")
	if err := os.WriteFile(stalePath, []byte("leftover from a previous install"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensurePFAlias(prefix, "linux"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(stalePath); err != nil {
		t.Fatalf("stale pf file was not replaced by a symlink: %v", err)
	}
}
