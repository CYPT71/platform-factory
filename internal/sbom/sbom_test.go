package sbom

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/assemble"
	"github.com/CYPT71/platform-factory/internal/cache"
	"github.com/CYPT71/platform-factory/internal/core"
)

func testPipelineWithOneOutput() core.Pipeline {
	return core.Pipeline{
		Outputs: []core.Output{{Name: "app", Stage: "build", Artifact: "binary"}},
	}
}

func TestGenerateParsesSupportedPackageMetadata(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.sum": writeTestFile(t, root, "go.sum", []byte(
			"example.com/module v1.2.3 h1:digest\nexample.com/module v1.2.3/go.mod h1:digest\n")),
		"Cargo.lock": writeTestFile(t, root, "Cargo.lock", []byte(
			"[[package]]\nname = \"serde\"\nversion = \"1.0.0\"\n")),
		"Gemfile.lock": writeTestFile(t, root, "Gemfile.lock", []byte(
			"GEM\n    rack (3.0.0)\n")),
		"package-lock.json": writeTestFile(t, root, "package-lock.json", []byte(
			`{"packages":{"node_modules/lodash":{"version":"4.17.21"}}}`)),
		"composer.lock": writeTestFile(t, root, "composer.lock", []byte(
			`{"packages":[{"name":"vendor/package","version":"1.4.0"}]}`)),
		"packages.lock.json": writeTestFile(t, root, "packages.lock.json", []byte(
			`{"dependencies":{"net8.0":{"Newtonsoft.Json":{"resolved":"13.0.3"}}}}`)),
	}
	dpkgDir := filepath.Join(root, "var", "lib", "dpkg")
	apkDir := filepath.Join(root, "lib", "apk", "db")
	for _, directory := range []string{dpkgDir, apkDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files["var/lib/dpkg/status"] = writeTestFile(t, dpkgDir, "status",
		[]byte("Package: libc6\nVersion: 2.39\n\n"))
	files["lib/apk/db/installed"] = writeTestFile(t, apkDir, "installed",
		[]byte("P:musl\nV:1.2.5-r0\n\n"))

	document, err := Generate(files)
	if err != nil {
		t.Fatal(err)
	}
	var identities []string
	for _, item := range document.Packages {
		identities = append(identities, item.Ecosystem+":"+item.Name+"@"+item.Version)
	}
	joined := strings.Join(identities, "\n")
	for _, want := range []string{
		"apk:musl@1.2.5-r0", "cargo:serde@1.0.0", "composer:vendor/package@1.4.0",
		"deb:libc6@2.39", "gem:rack@3.0.0", "go:example.com/module@v1.2.3",
		"npm:lodash@4.17.21", "nuget:Newtonsoft.Json@13.0.3",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q from packages:\n%s", want, joined)
		}
	}
}

func TestPackageMetadataRejectsMalformedDocuments(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"package-lock.json", "packages.lock.json"} {
		path := writeTestFile(t, root, name, []byte(`{"broken"`))
		if _, err := inspectPackageMetadata(name, path); err == nil {
			t.Fatalf("%s accepted malformed JSON", name)
		}
	}
	if packages, err := inspectPackageMetadata("unknown", writeTestFile(t, root, "unknown.lock", nil)); err != nil || packages != nil {
		t.Fatalf("unknown metadata packages=%v err=%v", packages, err)
	}
}

func writeTestFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestGenerateProducesSortedDeterministicComponents(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"zeta":  writeTestFile(t, dir, "zeta", []byte("zeta content")),
		"alpha": writeTestFile(t, dir, "alpha", []byte("alpha content")),
	}

	doc, err := Generate(paths)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(doc.Components) != 2 || doc.Components[0].Name != "alpha" || doc.Components[1].Name != "zeta" {
		t.Fatalf("components=%+v", doc.Components)
	}

	var first, second bytes.Buffer
	if err := Write(&first, doc); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc2, err := Generate(paths)
	if err != nil {
		t.Fatalf("generate again: %v", err)
	}
	if err := Write(&second, doc2); err != nil {
		t.Fatalf("write: %v", err)
	}
	if first.String() != second.String() {
		t.Fatalf("output is not deterministic:\n%s\nvs\n%s", first.String(), second.String())
	}
}

func TestGenerateComputesCorrectDigestAndSize(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello sbom")
	path := writeTestFile(t, dir, "app", content)

	doc, err := Generate(map[string]string{"app": path})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sum := sha256.Sum256(content)
	want := "sha256:" + hex.EncodeToString(sum[:])
	got := doc.Components[0]
	if got.Digest != want || got.Size != int64(len(content)) {
		t.Fatalf("component=%+v want digest=%q size=%d", got, want, len(content))
	}
}

func TestGenerateReportsUnknownKindForPlainFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTestFile(t, dir, "data", []byte("just some bytes, not an executable"))

	doc, err := Generate(map[string]string{"data": path})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	component := doc.Components[0]
	if component.Kind != "unknown" || len(component.Evidence) == 0 {
		t.Fatalf("component=%+v", component)
	}
	if len(component.NativeDependencies) != 0 {
		t.Fatalf("expected no native dependencies for a plain file: %+v", component)
	}
}

func TestGenerateRecognizesRealELFBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("go build produces a native ELF binary only on linux")
	}
	dir := t.TempDir()
	source := writeTestFile(t, dir, "main.go", []byte("package main\nfunc main() {}\n"))
	binary := filepath.Join(dir, "app")
	cmd := exec.Command("go", "build", "-o", binary, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test binary: %v: %s", err, output)
	}

	doc, err := Generate(map[string]string{"app": binary})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	component := doc.Components[0]
	if component.Kind != "elf" {
		t.Fatalf("component=%+v", component)
	}
}

func TestGenerateFailsForMissingFile(t *testing.T) {
	if _, err := Generate(map[string]string{"app": filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestGenerateFailsForDirectory(t *testing.T) {
	if _, err := Generate(map[string]string{"app": t.TempDir()}); err == nil {
		t.Fatal("expected an error for a directory")
	}
}

func TestGenerateCorrelatesApplicationAndSystemPackages(t *testing.T) {
	root := t.TempDir()
	requirements := writeTestFile(t, root, "requirements.lock", []byte("flask==3.1.0\n"))
	dpkgDir := filepath.Join(root, "var", "lib", "dpkg")
	if err := os.MkdirAll(dpkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	status := writeTestFile(t, dpkgDir, "status", []byte("Package: libc6\nVersion: 2.39-0\n\n"))
	doc, err := Generate(map[string]string{
		"requirements.lock":   requirements,
		"var/lib/dpkg/status": status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Packages) != 2 {
		t.Fatalf("packages=%+v", doc.Packages)
	}
	if doc.Packages[0].Ecosystem != "deb" || doc.Packages[1].Ecosystem != "pypi" {
		t.Fatalf("packages are not canonical: %+v", doc.Packages)
	}
}

// TestGenerateComposesWithAssembleExtract proves sbom.Generate accepts
// internal/assemble.Extract's return value directly, with no adapter.
func TestGenerateComposesWithAssembleExtract(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	cacheDesc, err := store.Put(bytes.NewReader([]byte("binary content")))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	definition := testPipelineWithOneOutput()
	resolve := func(stage, artifact string) (core.Descriptor, bool) {
		if stage == "build" && artifact == "binary" {
			return core.Descriptor{Digest: cacheDesc.Digest, Size: cacheDesc.Size}, true
		}
		return core.Descriptor{}, false
	}

	paths, err := assemble.Extract(cache.NewStoreAdapter(store), definition, resolve, t.TempDir())
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	doc, err := Generate(paths)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(doc.Components) != 1 || doc.Components[0].Name != "app" {
		t.Fatalf("components=%+v", doc.Components)
	}
}
