package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CYPT71/platform-factory/internal/project"
)

func loadTestProject(t *testing.T, config string) project.Loaded {
	t.Helper()
	root := t.TempDir()
	name := filepath.Join(root, "pf.yaml")
	if err := os.WriteFile(name, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := project.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestNeedsRebuildDetectsMissingAndStaleLayouts(t *testing.T) {
	loaded := loadTestProject(t, "version: 1\nlanguage: compiled\nartifact: app\n")
	binary := filepath.Join(loaded.Root, "app")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	if rebuild, err := NeedsRebuild(loaded); err != nil || !rebuild {
		t.Fatalf("expected a missing layout to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}

	indexPath := filepath.Join(loaded.Output(), "index.json")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(indexPath, future, future); err != nil {
		t.Fatal(err)
	}
	if rebuild, err := NeedsRebuild(loaded); err != nil || rebuild {
		t.Fatalf("expected a layout newer than its binary not to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}

	staleFuture := future.Add(-2 * time.Hour)
	if err := os.Chtimes(indexPath, staleFuture, staleFuture); err != nil {
		t.Fatal(err)
	}
	if rebuild, err := NeedsRebuild(loaded); err != nil || !rebuild {
		t.Fatalf("expected a layout older than its binary to need a rebuild: rebuild=%v err=%v", rebuild, err)
	}
}

func TestRequiresFrozenInputs(t *testing.T) {
	noDeps := loadTestProject(t, "version: 1\nlanguage: compiled\nartifact: app\n")
	if RequiresFrozenInputs(noDeps) {
		t.Fatal("expected a project with no includes/shared deps to not require frozen inputs")
	}
	withInclude := loadTestProject(t, "version: 1\nlanguage: custom\nartifact: app\nfreeze_command: [true]\ninclude:\n  - {source: dep.txt, destination: /app/dep.txt}\n")
	if !RequiresFrozenInputs(withInclude) {
		t.Fatal("expected a project with an explicit include to require frozen inputs")
	}
}

func TestValidateBuildCapability(t *testing.T) {
	noProfile := loadTestProject(t, "version: 1\nlanguage: python\nartifact: app.py\n")
	if err := ValidateBuildCapability(noProfile); err != nil {
		t.Fatalf("expected no error for a project with no recorded profile (compatibility path): %v", err)
	}
	missingRuntime := loadTestProject(t, "version: 1\nlanguage: python\nprofile: python\nartifact: app.py\n")
	if err := ValidateBuildCapability(missingRuntime); err == nil {
		t.Fatal("expected an error for an interpreted profile with no runtime field")
	}
	withRuntime := loadTestProject(t, "version: 1\nlanguage: python\nprofile: python\nartifact: app.py\nruntime: /usr/bin/python3\n")
	if err := ValidateBuildCapability(withRuntime); err != nil {
		t.Fatalf("expected no error once runtime is set: %v", err)
	}
	compiled := loadTestProject(t, "version: 1\nlanguage: compiled\nprofile: static\nartifact: app\n")
	if err := ValidateBuildCapability(compiled); err != nil {
		t.Fatalf("expected no error for a compiled profile: %v", err)
	}
}

func TestProfile(t *testing.T) {
	for language, want := range map[string]string{
		"python": "python", "nodejs": "node", "java": "java", "dotnet": "dotnet",
		"ruby": "ruby", "php": "php", "compiled": "static", "cobol": "static",
	} {
		if got := Profile(language); got != want {
			t.Errorf("Profile(%q) = %q, want %q", language, got, want)
		}
	}
}

func TestWatchContainerNameIsStableAndSafe(t *testing.T) {
	loaded := loadTestProject(t, "version: 1\nlanguage: compiled\nartifact: app\n")
	name := WatchContainerName(loaded)
	if name == "" {
		t.Fatal("expected a non-empty container name")
	}
	if WatchContainerName(loaded) != name {
		t.Fatal("expected WatchContainerName to be stable across calls for the same project")
	}
}

func writeSourceTestFile(t *testing.T, name, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSourceNewerThanMissingPath(t *testing.T) {
	remaining := 100
	stale, err := sourceNewerThan(filepath.Join(t.TempDir(), "does-not-exist"), time.Now(), &remaining)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Fatal("a missing path should never be reported stale")
	}
}

func TestSourceNewerThanRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	writeSourceTestFile(t, path, "content", 0o644)
	builtAt := time.Now().Add(-time.Hour)

	remaining := 100
	stale, err := sourceNewerThan(path, builtAt, &remaining)
	if err != nil {
		t.Fatalf("newer file: unexpected error: %v", err)
	}
	if !stale {
		t.Fatal("a file modified after builtAt should be reported stale")
	}
	if remaining != 99 {
		t.Fatalf("remaining = %d, want 99", remaining)
	}

	remaining = 100
	stale, err = sourceNewerThan(path, time.Now().Add(time.Hour), &remaining)
	if err != nil {
		t.Fatalf("older file: unexpected error: %v", err)
	}
	if stale {
		t.Fatal("a file modified before builtAt should not be reported stale")
	}
}

func TestSourceNewerThanDirectory(t *testing.T) {
	freshDir := t.TempDir()
	writeSourceTestFile(t, filepath.Join(freshDir, "unchanged.txt"), "content", 0o644)
	remaining := 100
	stale, err := sourceNewerThan(freshDir, time.Now().Add(time.Hour), &remaining)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Fatal("a directory with only old files should not be reported stale")
	}

	staleDir := t.TempDir()
	writeSourceTestFile(t, filepath.Join(staleDir, "nested", "changed.txt"), "content", 0o644)
	remaining = 100
	stale, err = sourceNewerThan(staleDir, time.Now().Add(-time.Hour), &remaining)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Fatal("a directory with a newly modified nested file should be reported stale")
	}
}

func TestSourceNewerThanRespectsRemainingBudget(t *testing.T) {
	dir := t.TempDir()
	writeSourceTestFile(t, filepath.Join(dir, "a.txt"), "content", 0o644)
	writeSourceTestFile(t, filepath.Join(dir, "b.txt"), "content", 0o644)

	remaining := 0
	stale, err := sourceNewerThan(dir, time.Now().Add(-time.Hour), &remaining)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Fatal("a walk with zero remaining budget should stop before finding staleness")
	}
}
