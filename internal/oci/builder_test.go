package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CYPT71/secure-oci-base/internal/budget"
	"github.com/CYPT71/secure-oci-base/internal/layout"
)

func TestCopyStreamFileCyclesCallerBuffer(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "payload")
	payload := []byte("a payload larger than the deliberately tiny test buffer")
	if err := os.WriteFile(sourcePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	buffer := make([]byte, 7)
	if err := copyStreamFile(&output, streamFile{
		source: sourcePath,
		size:   int64(len(payload)),
	}, buffer); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), payload) {
		t.Fatal("cycled copy changed the payload")
	}

	if err := copyStreamFile(io.Discard, streamFile{inline: payload}, nil); err == nil {
		t.Fatal("empty copy buffer accepted")
	}
}

// TestBuildLeavesNoGoroutinesOrFilesBehind guards against a leak
// regression: Build() is fully synchronous today (no goroutines, every
// file it opens is closed before it returns), and this pins that
// property so it's caught immediately if a future change - streaming,
// buffered I/O, a worker pool - introduces one.
func TestBuildLeavesNoGoroutinesOrFilesBehind(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	before := runtime.NumGoroutine()
	beforeFDs := openFileDescriptorCount(t)

	if _, err := Build(Options{Binary: binary, Output: filepath.Join(dir, "image")}); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines leaked: before=%d after=%d", before, after)
	}
	if afterFDs := openFileDescriptorCount(t); afterFDs > beforeFDs {
		t.Fatalf("file descriptors leaked: before=%d after=%d", beforeFDs, afterFDs)
	}
}

func TestBuildStreamsLargeSparseInputWithBoundedHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("large streaming regression")
	}
	root := t.TempDir()
	binary := filepath.Join(root, "large-service")
	file, err := os.OpenFile(binary, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	const inputSize = int64(64 << 20)
	if err := file.Truncate(inputSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := Build(Options{
		Binary: binary, Output: filepath.Join(root, "image"), Compression: "fast",
	}); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 48<<20 {
		t.Fatalf("streaming a %d-byte input allocated %d heap bytes", inputSize, allocated)
	}
}

func TestBuildFailsClosedWhenWallClockBudgetIsExceeded(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Build(Options{
		Binary: binary, Output: filepath.Join(dir, "image"),
		// Any nonzero wall-clock budget is already exceeded by the time the
		// first file is streamed, since real time has passed since the
		// tracker started.
		Budget: budget.Budget{WallClock: time.Nanosecond},
	})
	if !budget.IsBudgetExceeded(err) {
		t.Fatalf("expected a budget-exceeded error, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "image")); !os.IsNotExist(err) {
		t.Fatalf("expected no output layout on budget failure, stat err=%v", err)
	}
}

func TestBuildWithoutBudgetIsUnaffected(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Binary: binary, Output: filepath.Join(dir, "image")}); err != nil {
		t.Fatalf("zero-value Budget must not enforce any limit: %v", err)
	}
}

func TestNormalizeRejectsUnknownCompression(t *testing.T) {
	options := Options{Binary: "service", Output: filepath.Join(t.TempDir(), "image"), Compression: "unknown"}
	if err := normalize(&options); err == nil {
		t.Fatal("unknown compression accepted")
	}
}

// openFileDescriptorCount is Linux-only (via /proc/self/fd, which is what
// CI actually runs on); it returns 0 elsewhere rather than skipping, so
// the goroutine check above - which is portable - still runs on every OS.
func openFileDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	return len(entries)
}

func TestBuildWritesValidLayout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")
	digest, err := Build(Options{Binary: binary, Output: output, Architecture: "amd64", ImageName: "example/service", Tag: "v1", Created: time.Unix(0, 0), Labels: map[string]string{"security.tls.minimum": "1.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("digest = %q", digest)
	}
	if got, err := os.ReadFile(filepath.Join(output, "oci-layout")); err != nil || string(got) != "{\"imageLayoutVersion\":\"1.0.0\"}\n" {
		t.Fatalf("oci-layout = %q, %v", got, err)
	}
	var idx index
	data, err := os.ReadFile(filepath.Join(output, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Manifests) != 1 || idx.Manifests[0].Digest != digest {
		t.Fatalf("unexpected index: %+v", idx)
	}
	if got := idx.Manifests[0].Annotations["org.opencontainers.image.ref.name"]; got != "example/service:v1" {
		t.Fatalf("reference = %q", got)
	}
	manifestData, err := os.ReadFile(blobPath(output, digest))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(blobPath(output, m.Config.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config.Config.User != "65532:65532" || config.Config.Entrypoint[0] != "/app/service" || config.RootFS.DiffIDs[0] == "" {
		t.Fatalf("unsafe config: %+v", config)
	}
	layer, err := os.Open(blobPath(output, m.Layers[0].Digest))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	gz, err := gzip.NewReader(layer)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string]int64{}
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		entries[h.Name] = h.Mode
	}
	for name, mode := range map[string]int64{"app/service": 0555, "etc/ssl/certs/": 0755, "tmp/": 01777, "var/tmp/": 01777} {
		if entries[name] != mode {
			t.Fatalf("%s mode = %#o, want %#o", name, entries[name], mode)
		}
	}
}

func TestBuildWritesDeclarativeRuntimeConfig(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")
	digest, err := Build(Options{
		Binary: binary, Output: output, Entrypoint: "/app/service",
		Args: []string{"serve"}, WorkingDir: "/app",
		Env: map[string]string{"Z": "last", "A": "first"}, User: "10001:10001",
		Home: "/home/nonroot", IdentityFiles: true,
		Ports: []string{"8080/tcp"}, Volumes: []string{"/data"},
		WritablePaths: []string{"/data"},
		Healthcheck: &Healthcheck{
			Command:  []string{"/app/service", "health"},
			Interval: "30s", Timeout: "2s", Retries: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(blobPath(output, digest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(blobPath(output, manifest.Config.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var config imageConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		t.Fatal(err)
	}
	if config.Config.User != "10001:10001" ||
		strings.Join(config.Config.Cmd, " ") != "serve" ||
		strings.Join(config.Config.Env, ",") != "A=first,HOME=/home/nonroot,Z=last" ||
		config.Config.Healthcheck.Interval != int64(30*time.Second) ||
		config.Config.Healthcheck.Timeout != int64(2*time.Second) ||
		config.Config.Healthcheck.Retries != 3 {
		t.Fatalf("runtime config = %+v", config.Config)
	}
	if _, ok := config.Config.ExposedPorts["8080/tcp"]; !ok {
		t.Fatal("declared port missing")
	}
	if _, ok := config.Config.Volumes["/data"]; !ok {
		t.Fatal("declared volume missing")
	}
}

func TestBuildEmitsTraceableLifecycleEvents(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("binary-data"), 0755); err != nil {
		t.Fatal(err)
	}
	var events []Event
	_, err := Build(Options{
		Binary: binary, Output: filepath.Join(dir, "image"),
		TraceID: "trace-test", Observer: func(event Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 5 {
		t.Fatalf("events = %d, want lifecycle events", len(events))
	}
	if first := events[0]; first.Phase != "validate" || first.TraceID != "trace-test" {
		t.Fatalf("first event = %+v", first)
	}
	last := events[len(events)-1]
	if last.Level != "info" || last.Phase != "complete" || last.Fields["manifest_digest"] == "" {
		t.Fatalf("last event = %+v", last)
	}
}

func TestBuildWithExtraFilesPackagesADynamicallyLinkedStyleBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "legacy")
	if err := os.WriteFile(binary, []byte("elf-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	interp := filepath.Join(dir, "ld.so")
	if err := os.WriteFile(interp, []byte("dynamic-linker"), 0755); err != nil {
		t.Fatal(err)
	}
	libc := filepath.Join(dir, "libc.so.6")
	if err := os.WriteFile(libc, []byte("libc-bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")

	digest, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", Entrypoint: "/app/legacy",
		ExtraFiles: []ExtraFile{
			{Dest: "/lib64/ld-linux-x86-64.so.2", Source: interp},
			{Dest: "/lib/x86_64-linux-gnu/libc.so.6", Source: libc, Mode: 0444},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	manifestData, err := os.ReadFile(blobPath(output, digest))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		t.Fatal(err)
	}
	layer, err := os.Open(blobPath(output, m.Layers[0].Digest))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	gz, err := gzip.NewReader(layer)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	entries := map[string]int64{}
	contents := map[string]string{}
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		entries[h.Name] = h.Mode
		data, _ := io.ReadAll(tr)
		contents[h.Name] = string(data)
	}
	wantModes := map[string]int64{
		"app/legacy":                     0555,
		"lib64/":                         0755,
		"lib64/ld-linux-x86-64.so.2":     0555,
		"lib/x86_64-linux-gnu/":          0755,
		"lib/x86_64-linux-gnu/libc.so.6": 0444,
	}
	for name, mode := range wantModes {
		if entries[name] != mode {
			t.Fatalf("%s mode = %#o, want %#o (entries=%v)", name, entries[name], mode, entries)
		}
	}
	if contents["lib64/ld-linux-x86-64.so.2"] != "dynamic-linker" {
		t.Fatalf("interpreter content = %q", contents["lib64/ld-linux-x86-64.so.2"])
	}
	if contents["lib/x86_64-linux-gnu/libc.so.6"] != "libc-bytes" {
		t.Fatalf("libc content = %q", contents["lib/x86_64-linux-gnu/libc.so.6"])
	}
}

func TestBuildIsDeterministicWithExtraFiles(t *testing.T) {
	t.Parallel()
	build := func(dir string) string {
		binary := filepath.Join(dir, "legacy")
		_ = os.WriteFile(binary, []byte("elf-binary"), 0755)
		lib := filepath.Join(dir, "libfoo.so")
		_ = os.WriteFile(lib, []byte("lib-bytes"), 0755)
		digest, err := Build(Options{
			Binary: binary, Output: filepath.Join(dir, "image"), Architecture: "amd64",
			ExtraFiles: []ExtraFile{{Dest: "/lib/libfoo.so", Source: lib}},
		})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(blobPath(filepath.Join(dir, "image"), digest))
		if err != nil {
			t.Fatal(err)
		}
		return digest + string(data)
	}
	a := build(t.TempDir())
	b := build(t.TempDir())
	if a != b {
		t.Fatal("build with extra files is not deterministic across two independent builds")
	}
}

// readManifest reads and decodes the manifest blob for digest under output.
func readManifest(t *testing.T, output, digest string) manifest {
	t.Helper()
	data, err := os.ReadFile(blobPath(output, digest))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// readConfig reads and decodes the image config blob.
func readConfig(t *testing.T, output string, m manifest) imageConfig {
	t.Helper()
	data, err := os.ReadFile(blobPath(output, m.Config.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var c imageConfig
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// readLayerFiles extracts every regular file entry (path -> content) and
// every entry's mode (path -> mode, including directories) from one gzip
// tar layer blob.
func readLayerFiles(t *testing.T, output string, layerDigest string) (map[string]int64, map[string][]byte) {
	t.Helper()
	layer, err := os.Open(blobPath(output, layerDigest))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	gz, err := gzip.NewReader(layer)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	modes := map[string]int64{}
	contents := map[string][]byte{}
	for {
		h, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		modes[h.Name] = h.Mode
		if h.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			contents[h.Name] = data
		}
	}
	return modes, contents
}

// flattenLayers merges every layer's regular-file contents, in layer
// order, into one path -> content map, as a union filesystem mount would.
func flattenLayers(t *testing.T, output string, m manifest) map[string]string {
	t.Helper()
	flattened := map[string]string{}
	for _, layerDesc := range m.Layers {
		_, contents := readLayerFiles(t, output, layerDesc.Digest)
		for name, data := range contents {
			flattened[name] = string(data)
		}
	}
	return flattened
}

func semanticLayersTestInputs(t *testing.T, dir string) (binary, toolchain, dependency string) {
	t.Helper()
	binary = filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("app-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	toolchain = filepath.Join(dir, "runtime")
	if err := os.WriteFile(toolchain, []byte("bundled-runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	dependency = filepath.Join(dir, "libfoo.so")
	if err := os.WriteFile(dependency, []byte("lib-bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	return binary, toolchain, dependency
}

func TestBuildSingleLayerByDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary, toolchain, dependency := semanticLayersTestInputs(t, dir)
	output := filepath.Join(dir, "image")
	digest, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", IdentityFiles: true,
		ExtraFiles: []ExtraFile{
			{Dest: "/opt/runtime", Source: toolchain, Category: CategoryToolchain},
			{Dest: "/lib/libfoo.so", Source: dependency, Category: CategoryDependencies},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, output, digest)
	c := readConfig(t, output, m)
	if len(m.Layers) != 1 || len(c.RootFS.DiffIDs) != 1 {
		t.Fatalf("layers=%d diffIDs=%d, want 1 each (SemanticLayers unset)", len(m.Layers), len(c.RootFS.DiffIDs))
	}
}

func TestBuildProducesSemanticLayers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary, toolchain, dependency := semanticLayersTestInputs(t, dir)
	output := filepath.Join(dir, "image")
	digest, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", IdentityFiles: true, SemanticLayers: true,
		ExtraFiles: []ExtraFile{
			{Dest: "/opt/runtime", Source: toolchain, Category: CategoryToolchain},
			{Dest: "/lib/libfoo.so", Source: dependency, Category: CategoryDependencies},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, output, digest)
	c := readConfig(t, output, m)
	if len(m.Layers) != 4 || len(c.RootFS.DiffIDs) != 4 {
		t.Fatalf("layers=%d diffIDs=%d, want 4 (toolchain, dependencies, application, metadata)", len(m.Layers), len(c.RootFS.DiffIDs))
	}

	// Fixed order: toolchain, dependencies, application, metadata.
	_, toolchainFiles := readLayerFiles(t, output, m.Layers[0].Digest)
	if _, ok := toolchainFiles["opt/runtime"]; !ok {
		t.Fatalf("layer 0 (toolchain) missing opt/runtime: %v", toolchainFiles)
	}
	_, depFiles := readLayerFiles(t, output, m.Layers[1].Digest)
	if _, ok := depFiles["lib/libfoo.so"]; !ok {
		t.Fatalf("layer 1 (dependencies) missing lib/libfoo.so: %v", depFiles)
	}
	_, appFiles := readLayerFiles(t, output, m.Layers[2].Digest)
	if _, ok := appFiles["app/service"]; !ok {
		t.Fatalf("layer 2 (application) missing app/service: %v", appFiles)
	}
	_, metaFiles := readLayerFiles(t, output, m.Layers[3].Digest)
	if _, ok := metaFiles["etc/passwd"]; !ok {
		t.Fatalf("layer 3 (metadata) missing etc/passwd: %v", metaFiles)
	}
}

func TestBuildSemanticLayersOmitsEmptyCategories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("app-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")
	// No ExtraFiles, no IdentityFiles: only the application category has
	// content, so this must still be a single layer even with
	// SemanticLayers enabled.
	digest, err := Build(Options{Binary: binary, Output: output, Architecture: "amd64", SemanticLayers: true})
	if err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, output, digest)
	if len(m.Layers) != 1 {
		t.Fatalf("layers=%d, want 1 (only application category has files)", len(m.Layers))
	}
}

func TestBuildSemanticLayersPreservesFinalFilesystem(t *testing.T) {
	t.Parallel()
	buildBoth := func() (flat, split map[string]string) {
		dir := t.TempDir()
		binary, toolchain, dependency := semanticLayersTestInputs(t, dir)
		extraFiles := []ExtraFile{
			{Dest: "/opt/runtime", Source: toolchain, Category: CategoryToolchain},
			{Dest: "/lib/libfoo.so", Source: dependency, Category: CategoryDependencies},
		}

		singleOutput := filepath.Join(dir, "single")
		singleDigest, err := Build(Options{
			Binary: binary, Output: singleOutput, Architecture: "amd64", IdentityFiles: true,
			ExtraFiles: extraFiles,
		})
		if err != nil {
			t.Fatal(err)
		}
		flat = flattenLayers(t, singleOutput, readManifest(t, singleOutput, singleDigest))

		splitOutput := filepath.Join(dir, "split")
		splitDigest, err := Build(Options{
			Binary: binary, Output: splitOutput, Architecture: "amd64", IdentityFiles: true, SemanticLayers: true,
			ExtraFiles: extraFiles,
		})
		if err != nil {
			t.Fatal(err)
		}
		split = flattenLayers(t, splitOutput, readManifest(t, splitOutput, splitDigest))
		return flat, split
	}

	flat, split := buildBoth()
	if len(flat) != len(split) {
		t.Fatalf("file count differs: single-layer=%d semantic-layers=%d", len(flat), len(split))
	}
	for name, content := range flat {
		if split[name] != content {
			t.Fatalf("%s: single-layer content %q, semantic-layers content %q", name, content, split[name])
		}
	}
}

func TestBuildSemanticLayersIsDeterministic(t *testing.T) {
	t.Parallel()
	build := func(dir string) string {
		binary, toolchain, dependency := semanticLayersTestInputs(t, dir)
		output := filepath.Join(dir, "image")
		digest, err := Build(Options{
			Binary: binary, Output: output, Architecture: "amd64", IdentityFiles: true, SemanticLayers: true,
			ExtraFiles: []ExtraFile{
				{Dest: "/opt/runtime", Source: toolchain, Category: CategoryToolchain},
				{Dest: "/lib/libfoo.so", Source: dependency, Category: CategoryDependencies},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		m := readManifest(t, output, digest)
		combined := digest
		for _, layerDesc := range m.Layers {
			data, err := os.ReadFile(blobPath(output, layerDesc.Digest))
			if err != nil {
				t.Fatal(err)
			}
			combined += layerDesc.Digest + string(data)
		}
		return combined
	}
	a := build(t.TempDir())
	b := build(t.TempDir())
	if a != b {
		t.Fatal("semantic-layer build is not deterministic across two independent builds")
	}
}

func TestBuildSemanticLayersPassesIndependentVerification(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary, toolchain, dependency := semanticLayersTestInputs(t, dir)
	output := filepath.Join(dir, "image")
	_, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", IdentityFiles: true, SemanticLayers: true,
		ExtraFiles: []ExtraFile{
			{Dest: "/opt/runtime", Source: toolchain, Category: CategoryToolchain},
			{Dest: "/lib/libfoo.so", Source: dependency, Category: CategoryDependencies},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := layout.Verify(output)
	if err != nil {
		t.Fatalf("independent verification of a semantically-layered layout failed: %v", err)
	}
	if !report.Valid || report.Manifests != 1 {
		t.Fatalf("report=%+v", report)
	}
}

// TestBuildRejectsUnknownCategoryRatherThanDroppingTheFile is a regression
// test for a real bug found during review: writeLayers only emits the
// four known categories in semanticLayerOrder, so an ExtraFile.Category
// typo (e.g. "toolchian") used to be silently absent from every layer —
// no error, no warning, just missing content in the built image.
// normalize() must now reject any non-empty, unrecognized category before
// Build ever gets that far.
func TestBuildRejectsUnknownCategoryRatherThanDroppingTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("app-binary"), 0755); err != nil {
		t.Fatal(err)
	}
	dependency := filepath.Join(dir, "libfoo.so")
	if err := os.WriteFile(dependency, []byte("lib-bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "image")
	_, err := Build(Options{
		Binary: binary, Output: output, Architecture: "amd64", SemanticLayers: true,
		ExtraFiles: []ExtraFile{{Dest: "/lib/libfoo.so", Source: dependency, Category: "toolchian"}},
	})
	if err == nil {
		t.Fatal("expected an error for an unrecognized category, not a silently incomplete image")
	}
	if !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("expected no output to be written on validation failure, stat err = %v", statErr)
	}
}

func TestBuildIsDeterministicPerCompressionMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"", "best", "fast"} {
		t.Run("mode="+mode, func(t *testing.T) {
			t.Parallel()
			build := func(dir string) string {
				binary := filepath.Join(dir, "service")
				if err := os.WriteFile(binary, []byte("app-binary-payload"), 0755); err != nil {
					t.Fatal(err)
				}
				output := filepath.Join(dir, "image")
				digest, err := Build(Options{Binary: binary, Output: output, Architecture: "amd64", Compression: mode})
				if err != nil {
					t.Fatal(err)
				}
				m := readManifest(t, output, digest)
				data, err := os.ReadFile(blobPath(output, m.Layers[0].Digest))
				if err != nil {
					t.Fatal(err)
				}
				return digest + string(data)
			}
			a := build(t.TempDir())
			b := build(t.TempDir())
			if a != b {
				t.Fatalf("compression mode %q is not byte-for-byte stable across two independent builds", mode)
			}
		})
	}
}

func TestCompressionModeChangesLayerDigestNotDiffID(t *testing.T) {
	// Compression level affects only the compressed layer bytes; the
	// uncompressed content digest (diffID) must stay identical across
	// modes, since it identifies the same filesystem content either way.
	// A differing layer digest across modes is expected, not a
	// reproducibility bug.
	t.Parallel()
	build := func(mode string) (layerDigest, diffID string) {
		dir := t.TempDir()
		binary := filepath.Join(dir, "service")
		if err := os.WriteFile(binary, []byte("app-binary-payload"), 0755); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(dir, "image")
		digest, err := Build(Options{Binary: binary, Output: output, Architecture: "amd64", Compression: mode})
		if err != nil {
			t.Fatal(err)
		}
		m := readManifest(t, output, digest)
		c := readConfig(t, output, m)
		return m.Layers[0].Digest, c.RootFS.DiffIDs[0]
	}
	bestLayer, bestDiffID := build("best")
	fastLayer, fastDiffID := build("fast")
	if bestDiffID != fastDiffID {
		t.Fatalf("diffID differs across compression modes: best=%q fast=%q (same content must hash the same uncompressed)", bestDiffID, fastDiffID)
	}
	if bestLayer == fastLayer {
		t.Fatalf("layer digest is identical across compression modes (best=%q); expected it to differ since compressed bytes differ", bestLayer)
	}
}

func TestBuildRejectsExtraFileEntrypointCollision(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := Build(Options{
		Binary: binary, Output: filepath.Join(dir, "image"),
		ExtraFiles: []ExtraFile{{Dest: "/app/service", Source: binary}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate extra file destination") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildRejectsExtraFileErrors(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "service")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		extra   ExtraFile
		wantErr string
	}{
		{"missing source", ExtraFile{Dest: "/lib/x.so", Source: filepath.Join(dir, "missing")}, "stat extra file"},
		{"directory source", ExtraFile{Dest: "/lib/x.so", Source: dir}, "must be a regular file"},
		{"relative dest", ExtraFile{Dest: "lib/x.so", Source: binary}, "absolute, clean container path"},
		{"unclean dest", ExtraFile{Dest: "/lib/../x.so", Source: binary}, "absolute, clean container path"},
		{"unknown category", ExtraFile{Dest: "/lib/x.so", Source: binary, Category: "toolchian"}, "unknown category"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(Options{Binary: binary, Output: filepath.Join(dir, tt.name), ExtraFiles: []ExtraFile{tt.extra}})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestExtraFilesFromPairs(t *testing.T) {
	files, err := ExtraFilesFromPairs([]string{"/lib/b.so=host/b.so", "/lib/a.so=host/a.so"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Dest != "/lib/a.so" || files[0].Source != "host/a.so" {
		t.Fatalf("files = %+v", files)
	}
	if _, err := ExtraFilesFromPairs([]string{"bad"}); err == nil {
		t.Fatal("malformed pair accepted")
	}
	if _, err := ExtraFilesFromPairs([]string{"/x=a", "/x=b"}); err == nil {
		t.Fatal("duplicate destination accepted")
	}
	if _, err := ExtraFilesFromPairs([]string{"relative=host/path"}); err == nil {
		t.Fatal("relative destination accepted")
	}
	if _, err := ExtraFilesFromPairs([]string{"/no-source="}); err == nil {
		t.Fatal("empty source accepted")
	}
}

func TestExtraFilesFromPairsCategories(t *testing.T) {
	files, err := ExtraFilesFromPairs([]string{
		"toolchain@/usr/bin/python3=host/python3",
		"dependencies@/app/deps/lib.py=host/lib.py",
		"/app/main.py=host/main.py",
	})
	if err != nil {
		t.Fatal(err)
	}
	byDest := map[string]string{}
	for _, file := range files {
		byDest[file.Dest] = file.Category
	}
	if byDest["/usr/bin/python3"] != CategoryToolchain ||
		byDest["/app/deps/lib.py"] != CategoryDependencies ||
		byDest["/app/main.py"] != "" {
		t.Fatalf("categories = %+v", byDest)
	}
	for _, bad := range []string{
		"warehouse@/app/file=host/file",
		"toolchain@relative=host/file",
		"toolchain@=host/file",
	} {
		if _, err := ExtraFilesFromPairs([]string{bad}); err == nil {
			t.Fatalf("invalid category pair accepted: %s", bad)
		}
	}
	if _, err := ExtraFilesFromPairs([]string{"toolchain@/x=a", "/x=b"}); err == nil {
		t.Fatal("duplicate destination across categories accepted")
	}
}

func TestBuildRejectsUnsafeInputs(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "binary")
	if err := os.WriteFile(binary, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if _, err := Build(Options{Binary: binary, Output: filepath.Join(dir, "out")}); err == nil || !strings.Contains(err.Error(), "not executable") {
			t.Fatalf("err = %v", err)
		}
	}
	if _, err := LabelsFromPairs([]string{"a=1", "a=2"}); err == nil {
		t.Fatal("duplicate labels accepted")
	}
}

func TestNormalizeValidation(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{"missing inputs", Options{}, "binary and output"},
		{"architecture", Options{Binary: "x", Output: t.TempDir() + "/out", Architecture: "386"}, "unsupported architecture"},
		{"operating system", Options{Binary: "x", Output: t.TempDir() + "/out", OS: "darwin"}, "unsupported operating system"},
		{"entrypoint", Options{Binary: "x", Output: t.TempDir() + "/out", Entrypoint: "app/service"}, "entrypoint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := normalize(&tt.options); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "binary")
	if err := os.WriteFile(file, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		options Options
		want    string
	}{
		{"missing binary", Options{Binary: filepath.Join(dir, "missing"), Output: filepath.Join(dir, "one")}, "stat binary"},
		{"directory binary", Options{Binary: dir, Output: filepath.Join(dir, "two")}, "regular file"},
		{"existing output", Options{Binary: file, Output: dir}, "output already exists"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Build(tt.options); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLabelsFromPairs(t *testing.T) {
	labels, err := LabelsFromPairs([]string{"z=last", "a=first=value"})
	if err != nil {
		t.Fatal(err)
	}
	if labels["a"] != "first=value" || labels["z"] != "last" {
		t.Fatalf("labels = %#v", labels)
	}
	if _, err := LabelsFromPairs([]string{"bad"}); err == nil {
		t.Fatal("malformed label accepted")
	}
}

func FuzzLabelsFromPairs(f *testing.F) {
	f.Add("security.tls.minimum=1.2")
	f.Add("invalid")
	f.Fuzz(func(t *testing.T, value string) {
		_, _ = LabelsFromPairs([]string{value})
	})
}

func FuzzEntrypointValidation(f *testing.F) {
	f.Add("/app/service")
	f.Add("../../escape")
	f.Fuzz(func(t *testing.T, entrypoint string) {
		dir := t.TempDir()
		options := Options{Binary: "service", Output: filepath.Join(dir, "image"), Entrypoint: entrypoint}
		_ = normalize(&options)
	})
}

func TestWriteLayoutErrors(t *testing.T) {
	if err := writeLayout("/dev/null", []byte("{}"), nil); err == nil {
		t.Fatal("invalid root accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blobs"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeLayout(dir, []byte("{}"), nil); err == nil {
		t.Fatal("file blobs path accepted")
	}
	for _, tt := range []struct {
		name  string
		setup func(string)
	}{
		{"layout path directory", func(root string) {
			if err := os.Mkdir(filepath.Join(root, "oci-layout"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"index path directory", func(root string) {
			if err := os.Mkdir(filepath.Join(root, "index.json"), 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"blob path directory", func(root string) {
			d := newDescriptor("x", []byte("data"))
			if err := os.Mkdir(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(d.Digest, "sha256:")), 0755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0755); err != nil {
				t.Fatal(err)
			}
			tt.setup(root)
			d := newDescriptor("x", []byte("data"))
			if err := writeLayout(root, []byte("{}"), []blob{{digest: d.Digest, data: []byte("data")}}); err == nil {
				t.Fatal("write error not returned")
			}
		})
	}
}

func blobPath(root, digest string) string {
	return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

func TestWriteLayoutRejectsInvalidDigest(t *testing.T) {
	for _, digest := range []string{
		"",
		"sha512:" + strings.Repeat("0", 64),
		"sha256:../escape",
		"sha256:" + strings.Repeat("g", 64),
	} {
		t.Run(digest, func(t *testing.T) {
			err := writeLayout(t.TempDir(), []byte("{}"), []blob{{digest: digest, data: []byte("data")}})
			if err == nil {
				t.Fatalf("writeLayout accepted invalid digest %q", digest)
			}
		})
	}
}
