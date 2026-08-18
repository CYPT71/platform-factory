package langplugin

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectReturnsNoMatchForEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	result, err := Inspect(root, Definition{Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	// A no-match result is a fresh zero-ish Inspection, not the
	// partially populated working value (Language must not leak
	// through).
	if result.Match || result.Language != "" || result.Profile != "" || len(result.Evidence) != 0 {
		t.Fatalf("result=%+v, want a zero-ish Inspection with Match: false", result)
	}
}

func TestInspectFailsWhenRootIsNotARealDirectory(t *testing.T) {
	t.Run("nonexistent path", func(t *testing.T) {
		_, err := Inspect(filepath.Join(t.TempDir(), "does-not-exist"), Definition{})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file.txt")
		mustWriteFile(t, path, "hello")
		_, err := Inspect(path, Definition{})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("symlink to a directory", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := Inspect(link, Definition{})
		if err == nil {
			t.Fatal("expected an error for a symlinked root")
		}
	})
}

func TestInspectDetectsMarkerManifestEntrypointAndBuildCommand(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "go.mod"), "module example\n")
	mustWriteFile(t, filepath.Join(root, "main.go"), "package main\nfunc main() {}\n")

	definition := Definition{
		Language:         "go",
		Profile:          "static",
		Markers:          []string{"go.mod"},
		SourceExtensions: []string{".go"},
		Manifests:        []string{"go.mod"},
		Entrypoints:      []string{"main.go"},
		Infer: func(root string, sources []string) (string, string) {
			return "go\x00build\x00-o\x00app\x00main.go", "app"
		},
	}
	result, err := Inspect(root, definition)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match || result.Language != "go" || result.Profile != "static" {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Evidence) != 1 || result.Evidence[0] != "go.mod" {
		t.Fatalf("evidence=%v, want [go.mod]", result.Evidence)
	}
	if result.Dependencies.Mode != "manifest" || result.Dependencies.Manifest != "go.mod" || result.Dependencies.Reason != "go.mod detected" {
		t.Fatalf("dependencies=%+v", result.Dependencies)
	}
	if result.Entrypoint != "main.go" {
		t.Fatalf("entrypoint=%q, want main.go", result.Entrypoint)
	}
	// Infer's non-empty artifact overrides the entrypoint-derived one.
	if result.Artifact != "app" {
		t.Fatalf("artifact=%q, want app", result.Artifact)
	}
	want := []string{"go", "build", "-o", "app", "main.go"}
	if len(result.BuildCommand) != len(want) {
		t.Fatalf("buildCommand=%v, want %v", result.BuildCommand, want)
	}
	for i, part := range want {
		if result.BuildCommand[i] != part {
			t.Fatalf("buildCommand=%v, want %v", result.BuildCommand, want)
		}
	}
}

func TestInspectFallsBackToSourceExtensionEvidenceWhenNoMarkerMatches(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "b.go"), "package main\n")
	mustWriteFile(t, filepath.Join(root, "a.go"), "package main\n")

	result, err := Inspect(root, Definition{SourceExtensions: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match {
		t.Fatal("expected a match from source-extension evidence")
	}
	want := []string{"a.go", "b.go"}
	if len(result.Evidence) != len(want) || result.Evidence[0] != want[0] || result.Evidence[1] != want[1] {
		t.Fatalf("evidence=%v, want %v", result.Evidence, want)
	}
	if result.Dependencies.Mode != "none" {
		t.Fatalf("dependencies=%+v, want mode none", result.Dependencies)
	}
}

func TestInspectDependencyModes(t *testing.T) {
	t.Run("unresolved with deduplicated imports", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "a.py"), "import numpy\nimport requests\n")
		mustWriteFile(t, filepath.Join(root, "b.py"), "import numpy\n")
		definition := Definition{
			SourceExtensions: []string{".py"},
			Imports: func(source string) ([]string, bool) {
				var found []string
				if strings.Contains(source, "numpy") {
					found = append(found, "numpy")
				}
				if strings.Contains(source, "requests") {
					found = append(found, "requests")
				}
				return found, false
			},
		}
		result, err := Inspect(root, definition)
		if err != nil {
			t.Fatal(err)
		}
		if result.Dependencies.Mode != "unresolved" {
			t.Fatalf("mode=%q, want unresolved", result.Dependencies.Mode)
		}
		want := []string{"numpy", "requests"}
		if len(result.Dependencies.Imports) != len(want) {
			t.Fatalf("imports=%v, want %v (duplicates should be compacted)", result.Dependencies.Imports, want)
		}
		for i, imp := range want {
			if result.Dependencies.Imports[i] != imp {
				t.Fatalf("imports=%v, want %v", result.Dependencies.Imports, want)
			}
		}
		if !strings.Contains(result.Dependencies.Reason, "external imports detected") {
			t.Fatalf("reason=%q", result.Dependencies.Reason)
		}
	})

	t.Run("unknown for dynamic imports", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "c.py"), "importlib.import_module('x')\n")
		definition := Definition{
			SourceExtensions: []string{".py"},
			Imports: func(source string) ([]string, bool) {
				return nil, strings.Contains(source, "importlib")
			},
		}
		result, err := Inspect(root, definition)
		if err != nil {
			t.Fatal(err)
		}
		if result.Dependencies.Mode != "unknown" {
			t.Fatalf("mode=%q, want unknown", result.Dependencies.Mode)
		}
		if !strings.Contains(result.Dependencies.Reason, "dynamic module loading detected") {
			t.Fatalf("reason=%q", result.Dependencies.Reason)
		}
	})

	t.Run("none when no imports and not dynamic", func(t *testing.T) {
		root := t.TempDir()
		mustWriteFile(t, filepath.Join(root, "d.py"), "print('hi')\n")
		definition := Definition{
			SourceExtensions: []string{".py"},
			Imports:          func(source string) ([]string, bool) { return nil, false },
		}
		result, err := Inspect(root, definition)
		if err != nil {
			t.Fatal(err)
		}
		if result.Dependencies.Mode != "none" {
			t.Fatalf("mode=%q, want none", result.Dependencies.Mode)
		}
		if !strings.Contains(result.Dependencies.Reason, "no external dependencies detected") {
			t.Fatalf("reason=%q", result.Dependencies.Reason)
		}
	})
}

func TestInspectIgnoresDirectoriesAndSymlinkSourcesAsEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real.go")
	mustWriteFile(t, real, "package main\n")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := Inspect(root, Definition{SourceExtensions: []string{".go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0] != "real.go" {
		t.Fatalf("evidence=%v, want only [real.go] (directory and symlink excluded)", result.Evidence)
	}
}

func TestInspectManifestGlobPicksFirstSortedMatch(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "app.py"), "print('hi')\n")
	mustWriteFile(t, filepath.Join(root, "zzz.lock"), "z")
	mustWriteFile(t, filepath.Join(root, "aaa.lock"), "a")

	definition := Definition{SourceExtensions: []string{".py"}, Manifests: []string{"*.lock"}}
	result, err := Inspect(root, definition)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dependencies.Manifest != "aaa.lock" {
		t.Fatalf("manifest=%q, want aaa.lock (first alphabetically)", result.Dependencies.Manifest)
	}
}

func TestInspectPropagatesReadDirError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0o755)

	if _, err := Inspect(root, Definition{}); err == nil {
		t.Fatal("expected ReadDir failure to propagate as an error")
	}
}

func TestInspectPropagatesReadFileErrorForSource(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}
	root := t.TempDir()
	source := filepath.Join(root, "secret.py")
	mustWriteFile(t, source, "import os\n")
	if err := os.Chmod(source, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(source, 0o644)

	if _, err := Inspect(root, Definition{SourceExtensions: []string{".py"}}); err == nil {
		t.Fatal("expected unreadable source file to produce an error")
	}
}

func TestWriteInspectionEncodesJSONToStdout(t *testing.T) {
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	result := Inspection{
		Match: true, Language: "go", Profile: "static",
		Dependencies: DependencyInspection{Mode: "none", Reason: "no external dependencies detected"},
	}
	writeErr := WriteInspection(result)
	w.Close()
	os.Stdout = original
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Inspection
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("output %q is not valid JSON: %v", data, err)
	}
	if !decoded.Match || decoded.Language != "go" || decoded.Dependencies.Mode != "none" {
		t.Fatalf("decoded=%+v", decoded)
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunInspectionSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-plugin")
	body := `{"match":true,"language":"fake","dependencies":{"mode":"none","reason":"no external dependencies detected"}}`
	writeScript(t, path, sprintfScript(body))

	result, err := RunInspection(path, "/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Match || result.Language != "fake" {
		t.Fatalf("result=%+v", result)
	}
}

func sprintfScript(body string) string {
	return "#!/bin/sh\ncat <<'EOF'\n" + body + "\nEOF\n"
}

func TestRunInspectionWrapsCommandFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failing-plugin")
	writeScript(t, path, "#!/bin/sh\nexit 3\n")

	_, err := RunInspection(path, "/whatever")
	if err == nil {
		t.Fatal("expected an error when the plugin binary exits non-zero")
	}
	if !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("err=%v, want it to mention the inspect subcommand", err)
	}
}

func TestRunInspectionWrapsDecodeError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage-plugin")
	writeScript(t, path, "#!/bin/sh\necho not-json\n")

	_, err := RunInspection(path, "/whatever")
	if err == nil {
		t.Fatal("expected an error when the plugin emits invalid JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("err=%v, want it to mention decoding", err)
	}
}

func TestInspectLoadedAggregatesOnlyMatches(t *testing.T) {
	withPluginDir(t)
	root := t.TempDir()

	matchSource := filepath.Join(t.TempDir(), "src-match")
	writeScript(t, matchSource, sprintfScript(`{"match":true,"language":"alpha","dependencies":{"mode":"none","reason":"no external dependencies detected"}}`))
	if _, err := Load("alpha", matchSource); err != nil {
		t.Fatal(err)
	}

	noMatchSource := filepath.Join(t.TempDir(), "src-nomatch")
	writeScript(t, noMatchSource, sprintfScript(`{"match":false}`))
	if _, err := Load("beta", noMatchSource); err != nil {
		t.Fatal(err)
	}

	results, err := InspectLoaded(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Language != "alpha" {
		t.Fatalf("results=%+v, want exactly one match for alpha", results)
	}
}

func TestInspectLoadedReturnsEmptyWhenNothingLoaded(t *testing.T) {
	withPluginDir(t)
	results, err := InspectLoaded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("results=%v, want none", results)
	}
}

func TestInspectLoadedPropagatesPluginFailure(t *testing.T) {
	withPluginDir(t)
	source := filepath.Join(t.TempDir(), "src-failing")
	writeScript(t, source, "#!/bin/sh\nexit 1\n")
	if _, err := Load("broken", source); err != nil {
		t.Fatal(err)
	}

	if _, err := InspectLoaded(t.TempDir()); err == nil {
		t.Fatal("expected a failing plugin to surface an error")
	}
}

func TestCompact(t *testing.T) {
	if got := compact(nil); got != nil {
		t.Fatalf("compact(nil)=%v, want nil", got)
	}
	if got := compact([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("compact single=%v", got)
	}
	got := compact([]string{"a", "a", "b", "b", "b", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("compact=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("compact=%v, want %v", got, want)
		}
	}
}

func TestInspectFileExistsHelper(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	mustWriteFile(t, file, "x")

	if !fileExists(file) {
		t.Fatal("expected a regular file to exist")
	}
	if fileExists(dir) {
		t.Fatal("a directory should not be reported as an existing file")
	}
	if fileExists(filepath.Join(dir, "missing")) {
		t.Fatal("a missing path should not be reported as an existing file")
	}
}
