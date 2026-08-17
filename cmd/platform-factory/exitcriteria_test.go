package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/internal/plugin"
)

// zigPluginBinary builds the third-party "zig" plugin module (which
// imports only sdk/plugin) once per test run, offline.
var zigPluginBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "q1-zig-plugin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "zig-adapter")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = filepath.Join("..", "..", "testdata", "plugins", "thirdparty")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build zig plugin: %w: %s", err, output)
	}
	return binary, nil
})

// installSignedZigPlugin writes a signed plugin directory and its
// trusted public key, returning the plugin dir and key file.
func installSignedZigPlugin(t *testing.T) (string, string) {
	t.Helper()
	binary, err := zigPluginBinary()
	if err != nil {
		t.Fatalf("build zig plugin: %v", err)
	}
	payload, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "zig-adapter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zig-adapter"), payload, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest := plugin.Manifest{
		APIVersion: plugin.ManifestAPIVersion, Name: "zig-adapter", Version: "v0.1.0",
		Capabilities: []string{"detect", "freeze", "plan"}, Executable: "zig-adapter",
		Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.Sign(private, "test-key"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, keyFile
}

// TestExitCriterionExternalPluginAddsLanguage is v2 exit criterion q1:
// an external plugin adds a language the core does not know ("zig")
// without recompiling platform-factory. It drives the plugin through the
// shipped project freeze command.
func TestExitCriterionExternalPluginAddsLanguage(t *testing.T) {
	dir, keyFile := installSignedZigPlugin(t)
	pluginDir := filepath.Dir(dir)

	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"), "language: zig\nartifact: app\n", 0o644)
	writeProjectTestFile(t, filepath.Join(root, "build.zig"), "const std = @import(\"std\");", 0o644)
	writeProjectTestFile(t, filepath.Join(root, "app"), "binary", 0o755)

	var executed []string
	execute := func(name string, args []string, _ string, _, _ io.Writer) error {
		executed = append(executed, name+" "+strings.Join(args, " "))
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runProject([]string{
		"freeze", "--config", filepath.Join(root, ".config_image.yaml"),
		"--plugin-dir", pluginDir, "--plugin-key", keyFile,
		"--allow-unsandboxed-plugin",
	}, &stdout, &stderr, execute, nil, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	// The plugin supplied the freeze command for a language the core has
	// no built-in adapter for.
	if len(executed) != 1 || executed[0] != "zig build --fetch" {
		t.Fatalf("executed=%v", executed)
	}
}

// TestExitCriterionPluginRefusedWithoutKey confirms the deny-by-default
// posture through the CLI: without the trusted key the signed plugin is
// refused, so the unknown language has no freeze adapter.
func TestExitCriterionPluginRefusedWithoutKey(t *testing.T) {
	dir, _ := installSignedZigPlugin(t)
	pluginDir := filepath.Dir(dir)
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"), "language: zig\nartifact: app\n", 0o644)
	var stdout, stderr bytes.Buffer
	code := runProject([]string{
		"freeze", "--config", filepath.Join(root, ".config_image.yaml"), "--plugin-dir", pluginDir,
	}, &stdout, &stderr, func(string, []string, string, io.Writer, io.Writer) error { return nil }, nil, nil)
	if code == 0 {
		t.Fatal("signed plugin accepted without a trusted key")
	}
}

// TestExitCriterionPipelineExecutableFromCLI is v2 exit criterion q2's
// harness and part of the "v2 stack reachable" goal: a real pipeline
// runs through the shipped binary and its two independent branches both
// complete. The scheduler's true-overlap guarantee is unit-proven in
// internal/pipeline; here we confirm the CLI drives the whole stack.
func TestExitCriterionPipelineExecutableFromCLI(t *testing.T) {
	work := t.TempDir()
	name := writePipelineFile(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pipeline", "run", "--sandbox", "off", "--workdir", work, name}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	journal, err := os.ReadFile(filepath.Join(work, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	// compile and test are the two independent branches; both must run.
	for _, branch := range []string{"compile", "test"} {
		if !strings.Contains(string(journal), `"id": "`+branch+`"`) {
			t.Fatalf("journal missing branch %s: %s", branch, journal)
		}
	}
}

// TestExitCriterionConformanceRunsOutsideRepo is v2 exit criterion q4:
// the conformance binary, with vectors embedded, validates a plugin
// from an empty working directory that contains none of this
// repository.
func TestExitCriterionConformanceRunsOutsideRepo(t *testing.T) {
	conformanceBinary := buildBinary(t, "./cmd/platform-factory-conformance")
	demoPlugin := buildBinary(t, "./cmd/platform-factory-plugin-demo")
	empty := t.TempDir()
	cmd := exec.Command(conformanceBinary, "plugin", demoPlugin)
	cmd.Dir = empty
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("conformance plugin run failed: %v: %s", err, output)
	}
	cmd = exec.Command(conformanceBinary, "vectors")
	cmd.Dir = empty
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("conformance vectors run failed: %v: %s", err, output)
	}
}

func buildBinary(t *testing.T, pkg string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-o", binary, pkg)
	cmd.Dir = filepath.Join("..", "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", pkg, err, output)
	}
	return binary
}
