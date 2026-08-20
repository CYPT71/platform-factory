package langplugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withPluginDir points Dir() at a fresh temp directory for the duration
// of the test, so these tests never touch the real user's home
// directory.
func withPluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(dirEnv, dir)
	return dir
}

func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFailsWithActionableMessageWhenNotLoaded(t *testing.T) {
	withPluginDir(t)
	_, err := Resolve("python")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestLoadThenResolveFindsIt(t *testing.T) {
	withPluginDir(t)
	source := filepath.Join(t.TempDir(), "my-python-plugin")
	writeFakeBinary(t, source)

	installedPath, err := Load("python", source)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := Resolve("python")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPath != installedPath {
		t.Fatalf("resolvedPath=%q installedPath=%q", resolvedPath, installedPath)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil || string(data) != "#!/bin/sh\necho fake\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestLoadRejectsMissingSource(t *testing.T) {
	withPluginDir(t)
	if _, err := Load("python", filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing source file")
	}
}

func TestLoadRejectsEmptyName(t *testing.T) {
	withPluginDir(t)
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakeBinary(t, source)
	if _, err := Load("", source); err == nil {
		t.Fatal("expected an error for an empty name")
	}
}

func TestRegistryRejectsUnsafePluginNames(t *testing.T) {
	withPluginDir(t)
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakeBinary(t, source)
	for _, name := range []string{"../escape", "Uppercase", "has space", ".hidden", "a/b"} {
		if _, err := Load(name, source); err == nil {
			t.Fatalf("unsafe name %q was accepted", name)
		}
		if err := Unload(name); err == nil {
			t.Fatalf("unsafe unload name %q was accepted", name)
		}
	}
}

func TestUnloadRemovesALoadedPlugin(t *testing.T) {
	withPluginDir(t)
	source := filepath.Join(t.TempDir(), "plugin")
	writeFakeBinary(t, source)
	if _, err := Load("python", source); err != nil {
		t.Fatal(err)
	}
	if err := Unload("python"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("python"); err == nil {
		t.Fatal("expected python to no longer resolve after unload")
	}
}

func TestUnloadingSomethingNeverLoadedIsNotAnError(t *testing.T) {
	withPluginDir(t)
	if err := Unload("never-loaded"); err != nil {
		t.Fatalf("unload of a never-loaded plugin should be a no-op, got: %v", err)
	}
}

func TestListReturnsSortedLoadedNames(t *testing.T) {
	withPluginDir(t)
	for _, name := range []string{"rust", "node", "python"} {
		source := filepath.Join(t.TempDir(), name)
		writeFakeBinary(t, source)
		if _, err := Load(name, source); err != nil {
			t.Fatal(err)
		}
	}
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"node", "python", "rust"}
	if len(names) != len(want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("names=%v want=%v", names, want)
		}
	}
}

func TestListOnAnEmptyOrMissingDirectoryIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(dirEnv, filepath.Join(dir, "does-not-exist-yet"))
	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("names=%v, want empty", names)
	}
}

func TestLoadTwiceOverwritesRatherThanErrors(t *testing.T) {
	withPluginDir(t)
	first := filepath.Join(t.TempDir(), "v1")
	writeFakeBinary(t, first)
	second := filepath.Join(t.TempDir(), "v2")
	if err := os.WriteFile(second, []byte("v2 content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("python", first); err != nil {
		t.Fatal(err)
	}
	installedPath, err := Load("python", second)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installedPath)
	if err != nil || string(data) != "v2 content" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

// TestLoadNamesTheOverrideEnvVarWhenTheDefaultDirectoryIsUnwritable covers
// the same "default writable path" blind spot F7 fixed for the operation
// journal and workload state store (cmd/platform-factory/lifecycle.go):
// a non-root container with no writable $HOME hits this exact failure for
// the language-plugin directory too, and the error used to say only
// "permission denied" with no path forward.
func TestLoadNamesTheOverrideEnvVarWhenTheDefaultDirectoryIsUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits don't block MkdirAll the same way on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permission bits, so this can't be forced without one")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	t.Setenv(dirEnv, filepath.Join(parent, "plugins"))

	source := filepath.Join(t.TempDir(), "plugin")
	writeFakeBinary(t, source)
	_, err := Load("python", source)
	if err == nil {
		t.Fatal("expected an error for an unwritable plugin directory")
	}
	if !strings.Contains(err.Error(), dirEnv) {
		t.Fatalf("error %q does not name %s as the override", err.Error(), dirEnv)
	}
}
