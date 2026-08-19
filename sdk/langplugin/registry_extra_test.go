package langplugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateNameRules(t *testing.T) {
	valid := []string{"python", "lang-go", "a.b_c-9", "node20"}
	invalid := []string{"", "Uppercase", "-leading", ".leading", "_leading", "has space", "a/b"}
	for _, name := range valid {
		if err := validateName(name); err != nil {
			t.Errorf("validateName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range invalid {
		if err := validateName(name); err == nil {
			t.Errorf("validateName(%q) = nil, want an error", name)
		}
	}
}

func TestDirUsesEnvOverride(t *testing.T) {
	t.Setenv(dirEnv, "/custom/plugin/dir")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != "/custom/plugin/dir" {
		t.Fatalf("dir=%q, want /custom/plugin/dir", dir)
	}
}

func TestDirDefaultsUnderHomeDirectory(t *testing.T) {
	t.Setenv(dirEnv, "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".platform-factory", "plugins")
	if dir != want {
		t.Fatalf("dir=%q, want %q", dir, want)
	}
}

func TestResolveErrorsWhenLoadedPathIsNotARegularFile(t *testing.T) {
	dir := withPluginDir(t)
	// A directory sitting where the binary should be: loaded-but-broken,
	// distinct from never-loaded.
	if err := os.MkdirAll(filepath.Join(dir, binaryName("python")), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("python")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a non-empty message")
	}
}

func TestResolveFindsAdjacentBinaryWhenNotInManagedDir(t *testing.T) {
	withPluginDir(t) // empty managed dir - forces the adjacent-binary fallback
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	adjacentDir := filepath.Dir(self)
	adjacentPath := filepath.Join(adjacentDir, binaryName("adjtestlang"))
	if _, statErr := os.Stat(adjacentPath); statErr == nil {
		t.Skip("adjacent binary already exists, refusing to overwrite it")
	}
	writeFakeBinary(t, adjacentPath)
	defer os.Remove(adjacentPath)

	path, err := Resolve("adjtestlang")
	if err != nil {
		t.Fatal(err)
	}
	if path != adjacentPath {
		t.Fatalf("path=%q, want %q", path, adjacentPath)
	}
}

func TestListIgnoresLoadingSuffixAndDirectories(t *testing.T) {
	dir := withPluginDir(t)
	// An install still in progress - must not be reported as loaded.
	if err := os.WriteFile(filepath.Join(dir, "platform-factory-lang-xyz.loading"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory that happens to match the naming prefix - must be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "platform-factory-lang-sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBinary(t, filepath.Join(dir, "platform-factory-lang-foo"))

	names, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "foo" {
		t.Fatalf("names=%v, want [foo]", names)
	}
}

func TestUnloadPropagatesUnexpectedRemovalErrors(t *testing.T) {
	dir := withPluginDir(t)
	// A non-empty directory where the binary should be: os.Remove fails
	// with something other than ErrNotExist, and that must surface.
	target := filepath.Join(dir, binaryName("python"))
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unload("python"); err == nil {
		t.Fatal("expected an error removing a non-empty directory")
	}
}
