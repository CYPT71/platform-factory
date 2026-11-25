package langplugin

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildRealELF cross-compiles a trivial Go program into a genuine Linux ELF
// binary at dest, the same way TestResolveRuntimeClosureAndArchMismatchOnRealELF
// below does - FindSharedObjects and FindLibrariesByPrefix both call elf.Open
// to validate a candidate, so a placeholder byte string will never satisfy
// them regardless of its filename.
func buildRealELF(t *testing.T, dest string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	srcDir := t.TempDir()
	mainGo := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-o", dest, mainGo)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build real ELF: %v\n%s", err, out)
	}
}

func TestParseRuntimeFlagsRequiresRootAndImageRoot(t *testing.T) {
	if _, _, _, err := ParseRuntimeFlags(nil); err == nil {
		t.Fatal("expected error when --root and --image-root are missing")
	}
	root, imageRoot, hint, err := ParseRuntimeFlags([]string{"--root", "/proj", "--image-root", "/img", "--interpreter", "/usr/bin/node"})
	if err != nil {
		t.Fatalf("ParseRuntimeFlags: %v", err)
	}
	if root != "/proj" || imageRoot != "/img" || hint != "/usr/bin/node" {
		t.Fatalf("got root=%q imageRoot=%q hint=%q", root, imageRoot, hint)
	}
}

func TestBuildELFSearchDirsAndResolveSharedLibrary(t *testing.T) {
	dirs := buildELFSearchDirs([]string{"$ORIGIN/../lib:/opt/custom"}, []string{"${ORIGIN}/libs"}, "/usr/local/bin")
	if len(dirs) != 3 {
		t.Fatalf("dirs=%+v", dirs)
	}
	if dirs[0].diskDir != "/usr/local/lib" || dirs[0].template != "../lib" {
		t.Fatalf("dirs[0]=%+v", dirs[0])
	}
	if dirs[1].diskDir != "/opt/custom" || dirs[1].template != "" {
		t.Fatalf("dirs[1]=%+v", dirs[1])
	}
	if dirs[2].diskDir != "/usr/local/bin/libs" || dirs[2].template != "libs" {
		t.Fatalf("dirs[2]=%+v", dirs[2])
	}

	root := t.TempDir()
	stdDir := filepath.Join(root, "usr", "lib", "aarch64-linux-gnu")
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdDir, "libc.so.6"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	imgPath, _, viaOrigin, _, err := resolveELFSharedLibrary(root, "libc.so.6", nil)
	if err != nil || viaOrigin || imgPath != "/usr/lib/aarch64-linux-gnu/libc.so.6" {
		t.Fatalf("imgPath=%q viaOrigin=%v err=%v", imgPath, viaOrigin, err)
	}
	if _, _, _, _, err := resolveELFSharedLibrary(root, "libmissing.so.1", nil); err == nil {
		t.Fatal("expected error for missing library")
	}
}

func TestCopyRuntimeTreeExcludesAndMaterializesSymlinks(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.WriteFile(filepath.Join(src, "keep.dll"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "drop.so"), []byte("drop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("keep.dll", filepath.Join(src, "alias.dll")); err != nil {
		t.Fatal(err)
	}
	excluded := map[string]bool{filepath.Join(src, "drop.so"): true}
	if err := CopyRuntimeTree(src, dst, excluded); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "drop.so")); !os.IsNotExist(err) {
		t.Fatalf("excluded file should not exist: %v", err)
	}
	info, err := os.Lstat(filepath.Join(dst, "alias.dll"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected symlink to be materialized as regular file, mode=%v", info.Mode())
	}
}

func TestResolveRuntimeClosureAndArchMismatchOnRealELF(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	srcDir := t.TempDir()
	mainGo := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	imageRoot := t.TempDir()
	amd64Bin := filepath.Join(imageRoot, "usr", "local", "bin", "app-amd64")
	arm64Bin := filepath.Join(imageRoot, "usr", "local", "lib", "libarm64.so")
	if err := os.MkdirAll(filepath.Dir(amd64Bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(arm64Bin), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		out, arch string
	}{
		{amd64Bin, "amd64"},
		{arm64Bin, "arm64"},
	} {
		cmd := exec.Command("go", "build", "-o", target.out, mainGo)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+target.arch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", target.arch, err, out)
		}
	}

	deps, err := ResolveRuntimeClosure(imageRoot, amd64Bin, "/usr/local/bin/app-amd64", nil, false, nil)
	if err != nil || len(deps) != 0 {
		t.Fatalf("deps=%v err=%v", deps, err)
	}
	if err := ProbeSharedObject(imageRoot, SharedObject{ImagePath: "/usr/local/lib/libarm64.so", DiskPath: arm64Bin}); err != nil {
		t.Fatalf("ProbeSharedObject: %v", err)
	}
	_, err = ResolveRuntimeClosure(
		imageRoot,
		amd64Bin,
		"/usr/local/bin/app-amd64",
		[]SharedObject{{ImagePath: "/usr/local/lib/libarm64.so", DiskPath: arm64Bin}},
		false,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "architecture mismatch") {
		t.Fatalf("expected architecture mismatch error, got %v", err)
	}
}

func TestWriteRuntimeManifestPrintsTheExactManifestAsJSON(t *testing.T) {
	manifest := RuntimeManifest{
		Runtime: "/usr/bin/python3.12",
		Include: []RuntimeIncludeEntry{{Source: "/usr/lib/libpython3.12.so.1.0", Destination: "/usr/lib/libpython3.12.so.1.0"}},
	}
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	writeErr := WriteRuntimeManifest(manifest)
	w.Close()
	os.Stdout = original
	if writeErr != nil {
		t.Fatalf("WriteRuntimeManifest: %v", writeErr)
	}
	printed, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RuntimeManifest
	if err := json.Unmarshal(printed, &decoded); err != nil {
		t.Fatalf("printed output is not valid JSON: %v\n%s", err, printed)
	}
	if decoded.Runtime != manifest.Runtime || len(decoded.Include) != 1 || decoded.Include[0] != manifest.Include[0] {
		t.Fatalf("decoded=%+v, want %+v", decoded, manifest)
	}
}

func TestFindSharedObjectsFindsRealELFAndSkipsNonSharedObjects(t *testing.T) {
	imageRoot := t.TempDir()
	buildRealELF(t, filepath.Join(imageRoot, "opt", "app", "lib", "libfoo.so.1"))
	if err := os.WriteFile(filepath.Join(imageRoot, "opt", "app", "lib", "readme.txt"), []byte("not a library"), 0o644); err != nil {
		t.Fatal(err)
	}
	found, err := FindSharedObjects(filepath.Join(imageRoot, "opt", "app"), imageRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ImagePath != "/opt/app/lib/libfoo.so.1" {
		t.Fatalf("found=%+v", found)
	}
}

func TestFindLibrariesByPrefixMatchesOnlyDeclaredPrefixesInStandardDirs(t *testing.T) {
	imageRoot := t.TempDir()
	libDir := filepath.Join(imageRoot, "lib", "x86_64-linux-gnu")
	buildRealELF(t, filepath.Join(libDir, "libicuuc.so.74"))
	buildRealELF(t, filepath.Join(libDir, "libunrelated.so.1"))
	found, err := FindLibrariesByPrefix(imageRoot, []string{"libicuuc.so."})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ImagePath != "/lib/x86_64-linux-gnu/libicuuc.so.74" {
		t.Fatalf("found=%+v", found)
	}
	if again, err := FindLibrariesByPrefix(imageRoot, []string{"libicuuc.so."}); err != nil || len(again) != 1 {
		t.Fatalf("repeat scan not idempotent: found=%+v err=%v", again, err)
	}
}

func TestRelRuntimePathReturnsSlashesAndFallsBackOnError(t *testing.T) {
	if got := RelRuntimePath("/image/root", "/image/root/usr/lib/libc.so.6"); got != "usr/lib/libc.so.6" {
		t.Fatalf("got=%q", got)
	}
	// filepath.Rel refuses a relative root against an absolute target; the
	// fallback must still return a usable, slash-normalized path rather
	// than propagating that error.
	if got := RelRuntimePath("relative/root", "/absolute/target"); got != "/absolute/target" {
		t.Fatalf("fallback got=%q", got)
	}
}

func TestResolveELFSharedLibraryViaOriginTemplate(t *testing.T) {
	root := t.TempDir()
	libDir := filepath.Join(root, "opt", "app", "bin", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libfoo.so.1"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs := buildELFSearchDirs(nil, []string{"${ORIGIN}/lib"}, filepath.Join("/opt", "app", "bin"))
	imgPath, diskPath, viaOrigin, relFromOrigin, err := resolveELFSharedLibrary(root, "libfoo.so.1", dirs)
	if err != nil {
		t.Fatalf("resolveELFSharedLibrary: %v", err)
	}
	if !viaOrigin || relFromOrigin != "lib/libfoo.so.1" {
		t.Fatalf("viaOrigin=%v relFromOrigin=%q", viaOrigin, relFromOrigin)
	}
	if imgPath != "/opt/app/bin/lib/libfoo.so.1" || diskPath != filepath.Join(libDir, "libfoo.so.1") {
		t.Fatalf("imgPath=%q diskPath=%q", imgPath, diskPath)
	}
}

func TestCopyRuntimeFileCopiesContentIntoNewNestedDestination(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "nested", "dest.bin")
	if err := CopyRuntimeFile(src, dest); err != nil {
		t.Fatalf("CopyRuntimeFile: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("data=%q", data)
	}
}

func TestCopyRuntimeFileRejectsNonRegularSource(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "dest.bin")
	if err := CopyRuntimeFile(src, dest); err == nil {
		t.Fatal("expected an error copying a directory as if it were a regular file")
	}
}

func TestCopyRuntimeFileRejectsMissingSource(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "dest.bin")
	if err := CopyRuntimeFile(filepath.Join(t.TempDir(), "does-not-exist"), dest); err == nil {
		t.Fatal("expected an error for a missing source file")
	}
}
