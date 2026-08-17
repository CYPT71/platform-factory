package langplugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInspectRejectsInvalidRoots(t *testing.T) {
	if _, err := Inspect(filepath.Join(t.TempDir(), "missing"), Definition{}); err == nil {
		t.Fatal("expected an error for a missing root")
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(file, Definition{}); err == nil {
		t.Fatal("expected an error when root is a regular file")
	}

	if runtime.GOOS != "windows" {
		parent := t.TempDir()
		target := filepath.Join(parent, "real")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(link, Definition{}); err == nil {
			t.Fatal("expected an error when root is a symlink")
		}
	}
}

func TestInspectReportsNoMatchWithoutEvidence(t *testing.T) {
	root := t.TempDir()
	result, err := Inspect(root, Definition{Language: "go", SourceExtensions: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Match {
		t.Fatalf("expected no match, got %+v", result)
	}
}

func TestInspectUsesMarkersBeforeSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(root, Definition{
		Language: "go", Markers: []string{"go.mod"}, SourceExtensions: []string{".go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match || len(result.Evidence) != 1 || result.Evidence[0] != "go.mod" {
		t.Fatalf("result=%+v", result)
	}
}

func TestInspectFallsBackToSourcesAsEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(root, Definition{Language: "python", SourceExtensions: []string{".py"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match || len(result.Evidence) != 1 || result.Evidence[0] != "main.py" {
		t.Fatalf("result=%+v", result)
	}
	if result.Dependencies.Mode != "none" {
		t.Fatalf("expected dependency mode 'none' without an Imports func, got %+v", result.Dependencies)
	}
}

func TestInspectDetectsManifestDependencyMode(t *testing.T) {
	root := t.TempDir()
	writeInspectFile(t, root, "main.go", "package main\n")
	writeInspectFile(t, root, "go.sum", "")
	result, err := Inspect(root, Definition{
		SourceExtensions: []string{".go"}, Manifests: []string{"go.sum"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Dependencies.Mode != "manifest" || result.Dependencies.Manifest != "go.sum" {
		t.Fatalf("dependencies=%+v", result.Dependencies)
	}
}

func TestInspectImportsDrivesDependencyMode(t *testing.T) {
	root := t.TempDir()
	writeInspectFile(t, root, "main.go", "content")

	dynamic := Definition{
		SourceExtensions: []string{".go"},
		Imports:          func(string) ([]string, bool) { return nil, true },
	}
	result, err := Inspect(root, dynamic)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dependencies.Mode != "unknown" {
		t.Fatalf("dynamic: dependencies=%+v", result.Dependencies)
	}

	unresolved := Definition{
		SourceExtensions: []string{".go"},
		Imports:          func(string) ([]string, bool) { return []string{"b", "a", "a"}, false },
	}
	result, err = Inspect(root, unresolved)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dependencies.Mode != "unresolved" || len(result.Dependencies.Imports) != 2 {
		t.Fatalf("unresolved: dependencies=%+v", result.Dependencies)
	}
	if result.Dependencies.Imports[0] != "a" || result.Dependencies.Imports[1] != "b" {
		t.Fatalf("expected sorted, deduplicated imports, got %v", result.Dependencies.Imports)
	}

	none := Definition{
		SourceExtensions: []string{".go"},
		Imports:          func(string) ([]string, bool) { return nil, false },
	}
	result, err = Inspect(root, none)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dependencies.Mode != "none" {
		t.Fatalf("none: dependencies=%+v", result.Dependencies)
	}
}

func TestInspectResolvesEntrypointAndInfer(t *testing.T) {
	root := t.TempDir()
	writeInspectFile(t, root, "main.go", "content")
	writeInspectFile(t, root, "cmd/app", "")

	result, err := Inspect(root, Definition{
		SourceExtensions: []string{".go"},
		Entrypoints:      []string{"cmd/app"},
		Infer: func(root string, sources []string) (string, string) {
			return "go\x00build\x00-o\x00app", "app"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entrypoint != "cmd/app" || result.Artifact != "app" {
		t.Fatalf("result=%+v", result)
	}
	want := []string{"go", "build", "-o", "app"}
	if len(result.BuildCommand) != len(want) {
		t.Fatalf("BuildCommand=%v", result.BuildCommand)
	}
	for i, part := range want {
		if result.BuildCommand[i] != part {
			t.Fatalf("BuildCommand=%v", result.BuildCommand)
		}
	}
}

func writeInspectFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeInspectionBinary(t *testing.T, path, jsonBody string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat <<'EOF'\n" + jsonBody + "\nEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunInspection(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-plugin")
	writeFakeInspectionBinary(t, binary, `{"match":true,"language":"go"}`)

	result, err := RunInspection(binary, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Match || result.Language != "go" {
		t.Fatalf("result=%+v", result)
	}

	if _, err := RunInspection(filepath.Join(dir, "does-not-exist"), dir); err == nil {
		t.Fatal("expected an error for a missing binary")
	}

	badBinary := filepath.Join(dir, "bad-plugin")
	writeFakeInspectionBinary(t, badBinary, `not json`)
	if _, err := RunInspection(badBinary, dir); err == nil {
		t.Fatal("expected an error for invalid JSON output")
	}
}

func TestInspectLoaded(t *testing.T) {
	t.Setenv(dirEnv, t.TempDir())
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}

	results, err := InspectLoaded(t.TempDir())
	if err != nil {
		t.Fatalf("no loaded plugins: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %v", results)
	}

	writeFakeInspectionBinary(t, filepath.Join(dir, binaryName("matcher")), `{"match":true,"language":"matcher"}`)
	writeFakeInspectionBinary(t, filepath.Join(dir, binaryName("skipper")), `{"match":false}`)

	root := t.TempDir()
	results, err = InspectLoaded(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Language != "matcher" {
		t.Fatalf("expected only the matching plugin's result, got %+v", results)
	}
}

func TestCompact(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"a"}, []string{"a"}},
		{[]string{"a", "a", "b", "b", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := compact(tc.in)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("compact(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if fileExists(file) {
		t.Fatal("expected fileExists to be false for a missing file")
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(file) {
		t.Fatal("expected fileExists to be true for a regular file")
	}
	if fileExists(dir) {
		t.Fatal("expected fileExists to be false for a directory")
	}
}
