package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsInterpreterBinaryName(t *testing.T) {
	accept := []string{"python3", "python3.12", "python3.9", "python3.12.1"}
	for _, name := range accept {
		if !isInterpreterBinaryName(name) {
			t.Errorf("expected %q to be accepted", name)
		}
	}
	// The real bug this guards against: python3.12-config is a shell
	// script utility (not the interpreter) that "prefer the longest
	// name" would otherwise pick over the real python3.12 binary.
	reject := []string{"python3.12-config", "python3-config", "2to3", "idle3", "python", "pythonic3", ""}
	for _, name := range reject {
		if isInterpreterBinaryName(name) {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestFindInterpreterPrefersMostVersionSpecificRealBinary(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// python3 is a symlink (as it is on real python:*-slim images);
	// python3.12 and python3.12-config are both real, regular files -
	// only python3.12 (matching isInterpreterBinaryName) should ever be
	// selected, never the longer "-config" utility name.
	write := func(name string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("python3.12", 0o755)
	write("python3.12-config", 0o755)
	if err := os.Symlink("python3.12", filepath.Join(bin, "python3")); err != nil {
		t.Fatal(err)
	}

	got, err := findInterpreter(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/usr/local/bin/python3.12" {
		t.Fatalf("got %q", got)
	}
}

func TestFindInterpreterHonorsExplicitHint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "opt", "myPython"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findInterpreter(root, "/opt/myPython")
	if err != nil || got != "/opt/myPython" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := findInterpreter(root, "/opt/does-not-exist"); err == nil {
		t.Fatal("expected an error for a hint that does not exist")
	}
}

func TestFindInterpreterFailsClosedWithoutAnyCandidate(t *testing.T) {
	root := t.TempDir()
	if _, err := findInterpreter(root, ""); err == nil {
		t.Fatal("expected an error when no python3* binary exists")
	}
}

func TestFindStandardLibraryDerivesPathFromInterpreterVersion(t *testing.T) {
	root := t.TempDir()
	stdlib := filepath.Join(root, "usr", "local", "lib", "python3.12")
	if err := os.MkdirAll(stdlib, 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath, diskPath, err := findStandardLibrary(root, "/usr/local/bin/python3.12")
	if err != nil {
		t.Fatal(err)
	}
	if imagePath != "/usr/local/lib/python3.12" {
		t.Fatalf("imagePath=%q", imagePath)
	}
	if diskPath != stdlib {
		t.Fatalf("diskPath=%q want=%q", diskPath, stdlib)
	}
}

func TestFindStandardLibraryFailsClosedWhenMissing(t *testing.T) {
	root := t.TempDir()
	if _, _, err := findStandardLibrary(root, "/usr/local/bin/python3.12"); err == nil {
		t.Fatal("expected an error when the standard library directory does not exist")
	}
}

func TestFindSharedObjectsWalksRecursively(t *testing.T) {
	root := t.TempDir()
	stdlib := filepath.Join(root, "usr", "local", "lib", "python3.12")
	dynload := filepath.Join(stdlib, "lib-dynload")
	if err := os.MkdirAll(dynload, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"_hashlib.cpython-312-x86_64-linux-gnu.so", "_ssl.cpython-312-x86_64-linux-gnu.so"} {
		if err := os.WriteFile(filepath.Join(dynload, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stdlib, "os.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := findSharedObjects(stdlib, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d .so files, want 2: %+v", len(found), found)
	}
	for _, module := range found {
		if filepath.Ext(module.imagePath) != ".so" {
			t.Fatalf("unexpected non-.so entry: %+v", module)
		}
	}
}

func TestCopyTreeExcludesGivenSourcePaths(t *testing.T) {
	source := t.TempDir()
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.WriteFile(filepath.Join(source, "keep.py"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "drop.so"), []byte("drop"), 0o755); err != nil {
		t.Fatal(err)
	}
	excluded := map[string]bool{filepath.Join(source, "drop.so"): true}
	if err := copyTree(source, dest, excluded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "keep.py")); err != nil {
		t.Fatalf("expected keep.py to be copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "drop.so")); !os.IsNotExist(err) {
		t.Fatalf("expected drop.so to be excluded, got err=%v", err)
	}
}

func TestCopyTreePreservesRelativeSymlinks(t *testing.T) {
	source := t.TempDir()
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.WriteFile(filepath.Join(source, "real.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.py", filepath.Join(source, "alias.py")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(source, dest, nil); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dest, "alias.py"))
	if err != nil || target != "real.py" {
		t.Fatalf("target=%q err=%v", target, err)
	}
}

func TestBuildSearchDirsExpandsOriginAndKeepsAbsoluteEntriesUnexpanded(t *testing.T) {
	dirs := buildSearchDirs([]string{"$ORIGIN/../lib"}, nil, "/usr/local/bin")
	if len(dirs) != 1 {
		t.Fatalf("got %d dirs, want 1: %+v", len(dirs), dirs)
	}
	// diskDir is only ever fed into filepath.Join before use (see
	// resolveSharedLibrary), which normalizes "bin/../lib" to "lib" on
	// its own regardless - only template must keep the literal ".." hop
	// (checked below), since that one is joined against /runtime later,
	// a different base than requestingImageDir.
	if dirs[0].diskDir != "/usr/local/lib" {
		t.Fatalf("diskDir=%q", dirs[0].diskDir)
	}
	// The real bug this guards against: computing this template from the
	// already-$ORIGIN-expanded diskDir instead of independently let
	// filepath.Join's own ".." cleanup silently cancel the very hop that
	// needs to survive into the final /runtime-relative destination.
	if dirs[0].template != "../lib" {
		t.Fatalf("template=%q, want \"../lib\"", dirs[0].template)
	}

	absolute := buildSearchDirs([]string{"/opt/vendor/lib"}, nil, "/usr/local/bin")
	if len(absolute) != 1 || absolute[0].diskDir != "/opt/vendor/lib" || absolute[0].template != "" {
		t.Fatalf("absolute=%+v", absolute)
	}

	multi := buildSearchDirs([]string{"/a:$ORIGIN/../b"}, []string{"/c"}, "/x")
	if len(multi) != 3 {
		t.Fatalf("got %d dirs from a colon-separated rpath + runpath, want 3: %+v", len(multi), multi)
	}
}

func TestResolveSharedLibraryPrefersOriginRelativeOverStandardSearch(t *testing.T) {
	root := t.TempDir()
	// A library reachable both via an $ORIGIN-relative RPATH entry and a
	// standard system directory - the RPATH-relative one must win, since
	// that is what the real dynamic linker would actually pick.
	must := func(dir string) string {
		full := filepath.Join(root, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	originDir := must("usr/local/lib")
	must("usr/lib/x86_64-linux-gnu")
	if err := os.WriteFile(filepath.Join(originDir, "libpython3.12.so.1.0"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/lib/x86_64-linux-gnu/libpython3.12.so.1.0"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	imagePath, _, viaOrigin, relFromOrigin, err := resolveSharedLibrary(root, "libpython3.12.so.1.0",
		[]originSearchDir{{diskDir: "/usr/local/bin/../lib", template: "../lib"}})
	if err != nil {
		t.Fatal(err)
	}
	if !viaOrigin {
		t.Fatal("expected the $ORIGIN-relative match to win")
	}
	if imagePath != "/usr/local/lib/libpython3.12.so.1.0" {
		t.Fatalf("imagePath=%q", imagePath)
	}
	if relFromOrigin != "../lib/libpython3.12.so.1.0" {
		t.Fatalf("relFromOrigin=%q", relFromOrigin)
	}
}

func TestResolveSharedLibraryFallsBackToStandardDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "usr", "lib", "x86_64-linux-gnu")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "libc.so.6"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath, diskPath, viaOrigin, relFromOrigin, err := resolveSharedLibrary(root, "libc.so.6", nil)
	if err != nil {
		t.Fatal(err)
	}
	if viaOrigin || relFromOrigin != "" {
		t.Fatalf("expected a standard-search match, viaOrigin=%v relFromOrigin=%q", viaOrigin, relFromOrigin)
	}
	if imagePath != "/usr/lib/x86_64-linux-gnu/libc.so.6" {
		t.Fatalf("imagePath=%q", imagePath)
	}
	if diskPath != filepath.Join(dir, "libc.so.6") {
		t.Fatalf("diskPath=%q", diskPath)
	}
}

func TestResolveSharedLibraryFailsClosedWhenNotFound(t *testing.T) {
	root := t.TempDir()
	if _, _, _, _, err := resolveSharedLibrary(root, "libnonexistent.so.1", nil); err == nil {
		t.Fatal("expected an error for a library that exists nowhere in the image")
	}
}

func TestMustRel(t *testing.T) {
	if got := mustRel("/a/b", "/a/b/c/d"); got != "c/d" {
		t.Fatalf("got %q", got)
	}
}

// TestResolveRuntimeClosureAgainstARealCrossCompiledELF exercises
// resolveRuntimeClosure against a genuine Linux/amd64 ELF binary (built
// on the fly the same way demo/validate-personas.sh cross-compiles a
// real target binary for its own build tests), rather than only ever
// running debug/elf against real files by hand.
//
// It only proves the empty-closure path: `go build` without cgo produces
// a statically linked binary with no PT_INTERP and no DT_NEEDED, and
// cross-compiling a genuinely *dynamically* linked ELF (to exercise the
// DT_NEEDED/RPATH walk itself) needs a cgo cross-linker this environment
// doesn't have. That specific path - RPATH-vs-absolute classification,
// libpython/libc/libm/the ELF interpreter resolution, the destination
// math - is proven instead by the real, recorded run against the actual
// python:3.12-slim image pulled from Docker Hub (see patch.md), which is
// a stronger, not weaker, form of evidence than a synthetic fixture
// would be. This test's job is narrower and complementary: confirm the
// closure walk itself doesn't panic or misbehave on a real ELF file, not
// a hand-rolled or copied one.
func TestResolveRuntimeClosureAgainstARealCrossCompiledELF(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH to cross-compile a real ELF fixture")
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	imageRoot := t.TempDir()
	binDir := filepath.Join(imageRoot, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "python3.12")
	cmd := exec.Command("go", "build", "-o", binary, source)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile fixture: %v\n%s", err, output)
	}

	dependencies, err := resolveRuntimeClosure(imageRoot, binary, "/usr/local/bin/python3.12", nil)
	if err != nil {
		t.Fatalf("resolveRuntimeClosure on a real static ELF: %v", err)
	}
	if len(dependencies) != 0 {
		t.Fatalf("a statically-linked Go binary has no PT_INTERP/DT_NEEDED, got %d: %+v", len(dependencies), dependencies)
	}
}
