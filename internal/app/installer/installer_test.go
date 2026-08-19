package installer

import (
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
		got := ParseComponents(input)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseComponents(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestResolveComponentsAlwaysIncludesCore(t *testing.T) {
	selected, err := ResolveComponents(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Key != "core" {
		t.Fatalf("selected=%+v", selected)
	}
}

func TestResolveComponentsDeduplicatesAndPreservesCatalogOrder(t *testing.T) {
	selected, err := ResolveComponents([]string{"distributed", "core", "builder", "distributed"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range selected {
		keys = append(keys, c.Key)
	}
	want := []string{"core", "builder", "distributed"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys=%v want=%v", keys, want)
	}
}

func TestResolveComponentsRejectsUnknownKey(t *testing.T) {
	if _, err := ResolveComponents([]string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown component")
	}
}

func TestBuildStepsFlattensBinariesForEachSelectedComponent(t *testing.T) {
	selected, err := ResolveComponents([]string{"microvm"})
	if err != nil {
		t.Fatal(err)
	}
	steps := BuildSteps(selected, "linux", "amd64")
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
		if s.Pkg != "./cmd/"+s.Name {
			t.Errorf("step %q pkg=%q", s.Name, s.Pkg)
		}
		if s.CGO {
			t.Errorf("step %q should not require cgo on linux", s.Name)
		}
	}
	want := []string{"platform-factory", "microvm-init", "microvm-initramfs", "platform-factory-runtime"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
}

func TestBuildStepsEnablesCGOOnlyOnTheMatchingNativeMacHost(t *testing.T) {
	selected, err := ResolveComponents(nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := BuildSteps(selected, "darwin", "arm64")
	if len(steps) != 1 || steps[0].Name != "platform-factory" {
		t.Fatalf("steps=%+v", steps)
	}
	wantCGO := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	if steps[0].CGO != wantCGO {
		t.Fatalf("cgo=%v want=%v (host=%s/%s)", steps[0].CGO, wantCGO, runtime.GOOS, runtime.GOARCH)
	}

	// Cross-building darwin/arm64 from a different host must never enable
	// cgo, regardless of what the host actually is.
	steps = BuildSteps(selected, "darwin", "riscv64")
	if steps[0].CGO {
		t.Fatalf("cgo=true for an architecture no host can match natively")
	}
}

func TestBinSuffix(t *testing.T) {
	if got := BinSuffix("windows"); got != ".exe" {
		t.Fatalf("BinSuffix(windows)=%q", got)
	}
	if got := BinSuffix("linux"); got != "" {
		t.Fatalf("BinSuffix(linux)=%q", got)
	}
}
