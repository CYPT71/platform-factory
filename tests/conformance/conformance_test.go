package conformance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/CYPT71/platform-factory/conformance"
)

// conformanceDir is the source-tree conformance package directory, relative
// to this test's own directory. The golden vectors and their generator both
// live there, not alongside this out-of-package test.
const conformanceDir = "../../conformance"

// TestVectorsMatchEngine fails when the golden vectors drift from the
// engine. Regenerate deliberately with:
//
//	PLATFORM_FACTORY_CONFORMANCE_WRITE=1 go test ./tests/conformance -run TestVectors
//
// Regeneration must never run in CI; a drifting vector is a
// compatibility break that has to be reviewed, not overwritten.
func TestVectorsMatchEngine(t *testing.T) {
	if os.Getenv("PLATFORM_FACTORY_CONFORMANCE_WRITE") == "1" {
		regenerateVectors(t)
	}
	results, err := conformance.RunVectors(os.DirFS(conformanceDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 6 {
		t.Fatalf("expected at least 6 vectors, got %d", len(results))
	}
	for _, result := range results {
		if !result.Passed {
			t.Errorf("%s: %s", result.Name, result.Detail)
		}
	}
}

func regenerateVectors(t *testing.T) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(conformanceDir, "vectors", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var vector conformance.Vector
		if err := json.Unmarshal(data, &vector); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		actual, err := conformance.Evaluate(vector.Pipeline)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		vector.Expect = actual
		updated, err := json.MarshalIndent(vector, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, append(updated, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmbeddedVectorsMatchSourceTree(t *testing.T) {
	embedded, err := conformance.RunVectors(conformance.EmbeddedVectors())
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range embedded {
		if !result.Passed {
			t.Errorf("embedded %s: %s", result.Name, result.Detail)
		}
	}
}

func TestBackendVectorsMatchExecutor(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh required")
	}
	results, err := conformance.RunBackend(os.DirFS(conformanceDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 4 {
		t.Fatalf("expected at least 4 backend vectors, got %d", len(results))
	}
	for _, result := range results {
		if !result.Passed {
			t.Errorf("%s: %s", result.Name, result.Detail)
		}
	}
}

func TestEmbeddedBackendVectorsMatchSourceTree(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh required")
	}
	embedded, err := conformance.RunBackend(conformance.EmbeddedBackendVectors())
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range embedded {
		if !result.Passed {
			t.Errorf("embedded %s: %s", result.Name, result.Detail)
		}
	}
}

func TestRunBackendRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vectors-backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `{"name": "x", "stage": {"id": "x", "command": {"executable": "/bin/true"}}, "expect": {"exit_code": 0, "stdout": "", "extra": true}}`
	if err := os.WriteFile(filepath.Join(dir, "vectors-backend", "bad.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := conformance.RunBackend(os.DirFS(dir)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestRunBackendRejectsEmptyCorpus(t *testing.T) {
	dir := t.TempDir()
	if _, err := conformance.RunBackend(os.DirFS(dir)); err == nil {
		t.Fatal("empty corpus accepted")
	}
}

var demoPluginPath = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "conformance-demo-plugin-*")
	if err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "platform-factory-plugin-demo")
	cmd := exec.Command("go", "build", "-o", binary, "github.com/CYPT71/platform-factory/cmd/platform-factory-plugin-demo")
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build demo plugin: %w: %s", err, output)
	}
	return binary, nil
})

func TestPluginProtocolConformanceAgainstDemoPlugin(t *testing.T) {
	binary, err := demoPluginPath()
	if err != nil {
		t.Fatal(err)
	}
	results, err := conformance.RunPlugin(context.Background(), binary)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 5 {
		t.Fatalf("results=%+v", results)
	}
	for _, result := range results {
		if !result.Passed {
			t.Errorf("%s: %s", result.Name, result.Detail)
		}
	}
}

func TestRunPluginFailsForNonPluginExecutable(t *testing.T) {
	results, err := conformance.RunPlugin(context.Background(), "/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	passed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		}
	}
	if passed == len(results) {
		t.Fatalf("a non-plugin executable passed every check: %+v", results)
	}
}
