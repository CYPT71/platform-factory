package dockersave

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeRuntimeScript writes an executable shell script at
// dir/name simulating the docker/podman CLI just enough for
// cliRuntimeClient: `load` reads and discards stdin, then exits with
// exitCode (writing message to stderr first if non-empty); `image
// exists <ref>`/`image inspect <ref>` exits 0 for any reference not
// containing "missing", 1 otherwise - mirroring podman's/docker's own
// exit-code-only reporting cliclient.go's doc comment describes.
func writeFakeRuntimeScript(t *testing.T, dir, name string, loadExitCode int, loadStderr string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this fake CLI runtime is a POSIX shell script")
	}
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"load\" ]; then\n" +
		"  cat >/dev/null\n"
	if loadStderr != "" {
		script += "  echo " + shellQuote(loadStderr) + " 1>&2\n"
	}
	script += "  exit " + itoa(loadExitCode) + "\n" +
		"fi\n" +
		"if [ \"$1\" = \"image\" ]; then\n" +
		"  ref=\"$3\"\n" +
		"  case \"$ref\" in\n" +
		"    *missing*) exit 1 ;;\n" +
		"    *) exit 0 ;;\n" +
		"  esac\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestCliRuntimeClientLoadArchiveSucceeds(t *testing.T) {
	dir := t.TempDir()
	binPath := writeFakeRuntimeScript(t, dir, "fake-runtime-ok", 0, "")
	client := &cliRuntimeClient{runtimeName: binPath}
	if err := client.LoadArchive(context.Background(), strings.NewReader("archive bytes")); err != nil {
		t.Fatalf("LoadArchive returned an error: %v", err)
	}
}

func TestCliRuntimeClientLoadArchiveSurfacesStderrOnFailure(t *testing.T) {
	dir := t.TempDir()
	binPath := writeFakeRuntimeScript(t, dir, "fake-runtime-fail", 1, "manifest.json: no such file")
	client := &cliRuntimeClient{runtimeName: binPath}
	err := client.LoadArchive(context.Background(), strings.NewReader("archive bytes"))
	if err == nil || !strings.Contains(err.Error(), "manifest.json: no such file") {
		t.Fatalf("err=%v, want the CLI's stderr surfaced", err)
	}
}

func TestCliRuntimeClientLoadArchiveFailsWithoutStderrOutput(t *testing.T) {
	dir := t.TempDir()
	binPath := writeFakeRuntimeScript(t, dir, "fake-runtime-fail-quiet", 1, "")
	client := &cliRuntimeClient{runtimeName: binPath}
	err := client.LoadArchive(context.Background(), strings.NewReader("archive bytes"))
	if err == nil {
		t.Fatal("expected an error for a non-zero exit code")
	}
	if strings.Contains(err.Error(), ": : ") {
		t.Fatalf("err=%v should not contain a redundant empty message separator", err)
	}
}

func TestCliRuntimeClientImageExistsUsesPodmanImageExistsSubcommand(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeScript(t, dir, "podman", 0, "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	client := &cliRuntimeClient{runtimeName: "podman"}

	exists, err := client.ImageExists(context.Background(), "example/service:present")
	if err != nil {
		t.Fatalf("ImageExists returned an unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true for a reference the fake podman reports present")
	}

	exists, err = client.ImageExists(context.Background(), "example/service:missing")
	if err != nil {
		t.Fatalf("ImageExists returned an unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for a reference the fake podman reports absent")
	}
}

func TestCliRuntimeClientImageExistsUsesImageInspectForNonPodmanRuntimes(t *testing.T) {
	dir := t.TempDir()
	writeFakeRuntimeScript(t, dir, "docker", 0, "")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	client := &cliRuntimeClient{runtimeName: "docker"}

	exists, err := client.ImageExists(context.Background(), "example/service:present")
	if err != nil {
		t.Fatalf("ImageExists returned an unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true for a reference the fake docker reports present")
	}

	exists, err = client.ImageExists(context.Background(), "example/service:missing")
	if err != nil {
		t.Fatalf("ImageExists returned an unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for a reference the fake docker reports absent")
	}
}

func TestCliRuntimeClientImageExistsReturnsFalseWithoutErrorWhenTheBinaryIsMissing(t *testing.T) {
	client := &cliRuntimeClient{runtimeName: filepath.Join(t.TempDir(), "no-such-runtime-binary")}
	exists, err := client.ImageExists(context.Background(), "example/service:v1")
	if err != nil {
		t.Fatalf("ImageExists must swallow a lookup/exec failure as \"not found\", got err=%v", err)
	}
	if exists {
		t.Fatal("expected exists=false when the runtime binary cannot even be run")
	}
}
