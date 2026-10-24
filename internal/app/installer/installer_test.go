package installer

import (
	"reflect"
	"runtime"
	"strings"
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
		if !strings.HasPrefix(s.Name, "platform-factory-lang-") && s.Pkg != "./cmd/"+s.Name {
			t.Errorf("step %q pkg=%q", s.Name, s.Pkg)
		}
		if s.CGO {
			t.Errorf("step %q should not require cgo on linux", s.Name)
		}
	}
	want := append([]string{"platform-factory"}, LanguageBinaries()...)
	want = append(want, "microvm-init", "microvm-initramfs", "platform-factory-runtime")
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
	if len(steps) != 9 || steps[0].Name != "platform-factory" {
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

func TestAllProfileIncludesEveryIntegrationAndFiltersLinuxOnlyBinaries(t *testing.T) {
	selected, err := ResolveComponents([]string{"all", "integrations", "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != len(Components) {
		t.Fatalf("components=%v", selected)
	}
	for _, target := range []struct{ os, arch string }{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		seen := map[string]bool{}
		for _, step := range BuildSteps(selected, target.os, target.arch) {
			if seen[step.Name] {
				t.Fatalf("duplicate %s", step.Name)
			}
			seen[step.Name] = true
			if strings.Contains(step.Name, "kubernetes") && step.Pkg != "./plugins/kubernetes/cmd/platform-factory-kubernetes" {
				t.Fatal(step)
			}
		}
		for _, name := range []string{"platform-factory-kubernetes", "platform-factory-kubevirt", "platform-factory-containerd", "platform-factory-lang-go", "platform-factory-lang-python", "platform-factory-packager"} {
			if !seen[name] {
				t.Fatalf("%s/%s missing %s", target.os, target.arch, name)
			}
		}
		nativeRuntime := target.os == "linux" && target.arch == "amd64"
		if seen["platform-factory-runtime"] != nativeRuntime || seen["containerd-shim-platform-factory-v1"] != nativeRuntime {
			t.Fatalf("unsupported runtime selection: %v", seen)
		}
	}
}
