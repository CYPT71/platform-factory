package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareSourceRejectsMissingPath(t *testing.T) {
	_, cleanup, err := PrepareSource(filepath.Join(t.TempDir(), "does-not-exist"))
	cleanup()
	if err == nil {
		t.Fatal("expected an error for a nonexistent --from path")
	}
}

func TestLocateBuiltinPluginBinaryRejectsUnknownLanguage(t *testing.T) {
	if _, err := LocateBuiltinPluginBinary("not-a-real-language"); err == nil {
		t.Fatal("expected an error for a language with no built-in plugin")
	}
}

func TestBuiltinLanguageList(t *testing.T) {
	got := BuiltinLanguageList()
	for _, lang := range BuiltinLanguages {
		if !strings.Contains(got, lang) {
			t.Fatalf("list=%q missing language %q", got, lang)
		}
	}
}

func TestPrepareTypeScriptPluginRequiresNodeOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := prepareTypeScriptPlugin(filepath.Join(t.TempDir(), "plugin.ts"))
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "node was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestBuildDotnetPluginRequiresDotnetOnPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := buildDotnetPlugin(filepath.Join(t.TempDir(), "Plugin.cs"))
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "dotnet SDK was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginRequiresTheInterpreterOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows before checking PATH")
	}
	t.Setenv("PATH", t.TempDir())
	_, cleanup, err := prepareScriptPlugin(filepath.Join(t.TempDir(), "plugin.py"), "python3", nil)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "was not found on PATH") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginSurfacesInterpreterProbeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows before checking PATH")
	}
	dir := t.TempDir()
	interpreter := filepath.Join(dir, "brokenpy")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\necho probe failed >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "plugin.py")
	if err := os.WriteFile(source, []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := prepareScriptPlugin(source, "brokenpy", []string{"--strict"})
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "cannot execute this plugin source") {
		t.Fatalf("err=%v", err)
	}
}

func TestPrepareScriptPluginStripsShebangAndWrapsInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("prepareScriptPlugin always errors on windows")
	}
	dir := t.TempDir()
	interpreter := filepath.Join(dir, "fakepy")
	if err := os.WriteFile(interpreter, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "plugin.py")
	if err := os.WriteFile(source, []byte("#!/usr/bin/env python3\nprint(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := prepareScriptPlugin(source, "fakepy", nil)
	defer cleanup()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, readErr := os.ReadFile(prepared)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(content), "env python3") {
		t.Fatalf("expected the original shebang to be stripped, got:\n%s", content)
	}
	if !strings.HasPrefix(string(content), "#!/usr/bin/env -S fakepy\n") {
		t.Fatalf("expected the new interpreter shebang, got:\n%s", content)
	}
	if !strings.Contains(string(content), "print(1)") {
		t.Fatalf("expected the original source body preserved, got:\n%s", content)
	}
	info, statErr := os.Stat(prepared)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected the prepared script to be executable, mode=%v", info.Mode())
	}
}
