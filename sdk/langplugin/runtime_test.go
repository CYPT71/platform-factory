package langplugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
