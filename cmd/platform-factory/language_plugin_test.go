package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func fakeResolver(path string) pluginResolver {
	return func(string) (string, error) { return path, nil }
}

func TestLanguagePluginLayerNoopWhenNotOptedIn(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app.py\n")
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("execute should never be called when language_plugin is unset")
		return nil
	}
	resolve := func(string) (string, error) {
		t.Fatal("resolve should never be called when language_plugin is unset")
		return "", nil
	}
	var stderr bytes.Buffer
	tarPath, cleanup, err := languagePluginLayer(loaded, &stderr, execute, resolve)
	if err != nil || tarPath != "" {
		t.Fatalf("tarPath=%q err=%v", tarPath, err)
	}
	cleanup() // must be safe to call even though nothing was created
}

func TestLanguagePluginLayerRejectsEmptyLanguage(t *testing.T) {
	loaded := loadProjectTest(t, "legacy_disks:\n  boot: disk.raw\nlanguage_plugin: true\n")
	if loaded.Config.Language != "" {
		t.Fatalf("test fixture assumption broken: Language=%q", loaded.Config.Language)
	}
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("execute should never be called with an empty language")
		return nil
	}
	resolve := func(string) (string, error) {
		t.Fatal("resolve should never be called with an empty language")
		return "", nil
	}
	var stderr bytes.Buffer
	if _, cleanup, err := languagePluginLayer(loaded, &stderr, execute, resolve); err == nil {
		cleanup()
		t.Fatal("expected an error for language_plugin set with no language")
	} else {
		cleanup()
	}
}

func TestLanguagePluginLayerPropagatesResolveFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app.py\nlanguage_plugin: true\n")
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		t.Fatal("execute should never be called when resolve fails")
		return nil
	}
	resolve := func(language string) (string, error) {
		return "", errors.New(`language plugin "python" isn't loaded - run ` + "`pf plugin load python`" + ` first`)
	}
	var stderr bytes.Buffer
	tarPath, cleanup, err := languagePluginLayer(loaded, &stderr, execute, resolve)
	defer cleanup()
	if err == nil || tarPath != "" {
		t.Fatalf("tarPath=%q err=%v", tarPath, err)
	}
	if !strings.Contains(err.Error(), "pf plugin load python") {
		t.Fatalf("err=%v, want an actionable message naming the fix", err)
	}
}

func TestLanguagePluginLayerPropagatesExecuteFailure(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app.py\nlanguage_plugin: true\n")
	execute := func(name string, args []string, dir string, _, _ io.Writer) error {
		if name != "/loaded/platform-factory-lang-python" {
			t.Fatalf("name=%q", name)
		}
		if len(args) < 1 || args[0] != "build-layer" {
			t.Fatalf("args=%v", args)
		}
		return os.ErrNotExist
	}
	var stderr bytes.Buffer
	tarPath, cleanup, err := languagePluginLayer(loaded, &stderr, execute, fakeResolver("/loaded/platform-factory-lang-python"))
	defer cleanup()
	if err == nil || tarPath != "" {
		t.Fatalf("tarPath=%q err=%v", tarPath, err)
	}
	if !strings.Contains(stderr.String(), "platform-factory-lang-python") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestLanguagePluginLayerReturnsTarPathOnSuccess(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app.py\nlanguage_plugin: true\n")
	var gotArgs []string
	execute := func(name string, args []string, dir string, _, _ io.Writer) error {
		gotArgs = args
		// Simulate the plugin actually producing its output file.
		outputIndex := -1
		for i, a := range args {
			if a == "--output" && i+1 < len(args) {
				outputIndex = i + 1
			}
		}
		if outputIndex == -1 {
			t.Fatal("no --output flag in args")
		}
		return os.WriteFile(args[outputIndex], []byte("fake tar"), 0o644)
	}
	var stderr bytes.Buffer
	tarPath, cleanup, err := languagePluginLayer(loaded, &stderr, execute, fakeResolver("/loaded/platform-factory-lang-python"))
	defer cleanup()
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, stderr.String())
	}
	if tarPath == "" {
		t.Fatal("expected a non-empty tar path")
	}
	if _, statErr := os.Stat(tarPath); statErr != nil {
		t.Fatalf("tar path does not exist: %v", statErr)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--dest app/.platform-factory/deps/python") {
		t.Fatalf("args=%v", gotArgs)
	}
	if !strings.Contains(joined, "--root "+loaded.Root) {
		t.Fatalf("args=%v, want --root %s", gotArgs, loaded.Root)
	}

	cleanup()
	if _, statErr := os.Stat(tarPath); statErr == nil {
		t.Fatal("cleanup should have removed the tar file")
	}
}

func TestLanguagePluginLayerErrorsWhenPluginDoesNotProduceOutput(t *testing.T) {
	loaded := loadProjectTest(t, "language: python\nartifact: app.py\nlanguage_plugin: true\n")
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		return nil // "succeeds" but never actually writes the output file
	}
	var stderr bytes.Buffer
	tarPath, cleanup, err := languagePluginLayer(loaded, &stderr, execute, fakeResolver("/loaded/platform-factory-lang-python"))
	defer cleanup()
	if err == nil || tarPath != "" {
		t.Fatalf("tarPath=%q err=%v, want an error for a missing output file", tarPath, err)
	}
}

func TestLanguagePluginDestPrefix(t *testing.T) {
	if got := languagePluginDestPrefix("python"); got != "app/.platform-factory/deps/python" {
		t.Fatalf("got=%q", got)
	}
}
