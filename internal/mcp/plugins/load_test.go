package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CYPT71/platform-factory/internal/mcp/toolerror"
)

// fakeSourceFile writes a regular file with an extension PrepareSource
// (internal/app/plugin) treats as an opaque, already-built binary (no
// .py/.js/.ts/.php/.cs handling kicks in), so Load's --from path can be
// exercised hermetically without a real interpreter or compiler on PATH.
func fakeSourceFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "my-plugin-binary")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSucceedsWithAFromSuppliedPathAndPassingProbe(t *testing.T) {
	source := fakeSourceFile(t)
	var loadedName, loadedSource string
	var inspectBinary, inspectRoot string
	backend := LanguagePluginBackend{
		Load: func(name, sourcePath string) (string, error) {
			loadedName, loadedSource = name, sourcePath
			return "/installed/" + name, nil
		},
		Inspect: func(binary, root string) error {
			inspectBinary, inspectRoot = binary, root
			return nil
		},
		Unload: func(name string) error {
			t.Fatalf("Unload must not be called on a successful probe, got name=%q", name)
			return nil
		},
	}

	result, err := Load(context.Background(), backend, LoadRequest{Name: "widget", From: source})
	if err != nil {
		t.Fatalf("Load returned an error: %v", err)
	}
	if result.Plugin != "widget" || result.InstalledPath != "/installed/widget" {
		t.Fatalf("result=%+v", result)
	}
	if loadedName != "widget" || loadedSource != source {
		t.Fatalf("backend.Load called with name=%q source=%q, want name=widget source=%s", loadedName, loadedSource, source)
	}
	if inspectBinary != "/installed/widget" {
		t.Fatalf("backend.Inspect called with binary=%q, want /installed/widget", inspectBinary)
	}
	if inspectRoot == "" {
		t.Fatal("backend.Inspect must be called with a non-empty probe root")
	}
	if _, statErr := os.Stat(inspectRoot); !os.IsNotExist(statErr) {
		t.Fatalf("the probe root must be cleaned up after Inspect returns, stat err=%v", statErr)
	}
}

func TestLoadRejectsAnEmptyName(t *testing.T) {
	backend := LanguagePluginBackend{
		Load:    func(string, string) (string, error) { t.Fatal("backend.Load must not be called"); return "", nil },
		Inspect: func(string, string) error { t.Fatal("backend.Inspect must not be called"); return nil },
		Unload:  func(string) error { t.Fatal("backend.Unload must not be called"); return nil },
	}
	for _, name := range []string{"", "   "} {
		_, err := Load(context.Background(), backend, LoadRequest{Name: name, From: fakeSourceFile(t)})
		if err == nil {
			t.Fatalf("Load(name=%q) expected an error", name)
		}
		var toolErr *toolerror.ToolError
		if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrInvalidArgument {
			t.Fatalf("Load(name=%q) error=%v, want a toolerror.ErrInvalidArgument", name, err)
		}
	}
}

func TestLoadFailsWhenNoFromIsGivenAndTheNameIsNotABuiltinLanguage(t *testing.T) {
	backend := LanguagePluginBackend{
		Load:    func(string, string) (string, error) { t.Fatal("backend.Load must not be called"); return "", nil },
		Inspect: func(string, string) error { t.Fatal("backend.Inspect must not be called"); return nil },
		Unload:  func(string) error { t.Fatal("backend.Unload must not be called"); return nil },
	}
	_, err := Load(context.Background(), backend, LoadRequest{Name: "not-a-real-builtin-language"})
	if err == nil {
		t.Fatal("expected an error locating a non-builtin language's binary")
	}
}

func TestLoadPropagatesABackendLoadFailure(t *testing.T) {
	source := fakeSourceFile(t)
	wantErr := errors.New("registry is full")
	backend := LanguagePluginBackend{
		Load: func(string, string) (string, error) { return "", wantErr },
		Inspect: func(string, string) error {
			t.Fatal("backend.Inspect must not be called when backend.Load fails")
			return nil
		},
		Unload: func(string) error {
			t.Fatal("backend.Unload must not be called when backend.Load fails")
			return nil
		},
	}
	_, err := Load(context.Background(), backend, LoadRequest{Name: "widget", From: source})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Load error=%v, want it to wrap %v", err, wantErr)
	}
}

func TestLoadUnloadsAndReturnsAValidationErrorWhenTheProbeFails(t *testing.T) {
	source := fakeSourceFile(t)
	var unloadedName string
	unloadCalled := false
	backend := LanguagePluginBackend{
		Load: func(name, sourcePath string) (string, error) { return "/installed/" + name, nil },
		Inspect: func(binary, root string) error {
			return errors.New("plugin does not speak the inspect protocol")
		},
		Unload: func(name string) error {
			unloadCalled = true
			unloadedName = name
			return nil
		},
	}
	_, err := Load(context.Background(), backend, LoadRequest{Name: "widget", From: source})
	if err == nil {
		t.Fatal("expected an error when the probe fails")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrValidationFailed {
		t.Fatalf("error=%v, want a toolerror.ErrValidationFailed", err)
	}
	if !unloadCalled {
		t.Fatal("backend.Unload must be called after a failed probe")
	}
	if unloadedName != "widget" {
		t.Fatalf("backend.Unload called with name=%q, want widget", unloadedName)
	}
}

func TestLoadToolHandlerRoundTripsValidArguments(t *testing.T) {
	source := fakeSourceFile(t)
	backend := LanguagePluginBackend{
		Load:    func(name, sourcePath string) (string, error) { return "/installed/" + name, nil },
		Inspect: func(string, string) error { return nil },
		Unload:  func(string) error { return nil },
	}
	handler := LoadToolHandler(backend)
	out, err := handler(context.Background(), json.RawMessage(`{"name":"widget","from":"`+source+`"}`))
	if err != nil {
		t.Fatalf("handler returned an error: %v", err)
	}
	var result LoadResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("handler output is not valid JSON: %v (%s)", err, out)
	}
	if result.Plugin != "widget" || result.InstalledPath != "/installed/widget" {
		t.Fatalf("result=%+v", result)
	}
}

func TestLoadToolHandlerRejectsInvalidJSON(t *testing.T) {
	backend := LanguagePluginBackend{}
	handler := LoadToolHandler(backend)
	_, err := handler(context.Background(), json.RawMessage(`{not json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON arguments")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrInvalidArgument {
		t.Fatalf("error=%v, want a toolerror.ErrInvalidArgument", err)
	}
}

func TestLoadToolHandlerRejectsUnknownFields(t *testing.T) {
	backend := LanguagePluginBackend{}
	handler := LoadToolHandler(backend)
	_, err := handler(context.Background(), json.RawMessage(`{"name":"widget","bogus":true}`))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrInvalidArgument {
		t.Fatalf("error=%v, want a toolerror.ErrInvalidArgument", err)
	}
}

func TestLoadToolHandlerRejectsAMissingName(t *testing.T) {
	backend := LanguagePluginBackend{
		Load:    func(string, string) (string, error) { t.Fatal("backend.Load must not be called"); return "", nil },
		Inspect: func(string, string) error { t.Fatal("backend.Inspect must not be called"); return nil },
		Unload:  func(string) error { t.Fatal("backend.Unload must not be called"); return nil },
	}
	handler := LoadToolHandler(backend)
	_, err := handler(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error for a missing name")
	}
	var toolErr *toolerror.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != toolerror.ErrInvalidArgument {
		t.Fatalf("error=%v, want a toolerror.ErrInvalidArgument", err)
	}
}
