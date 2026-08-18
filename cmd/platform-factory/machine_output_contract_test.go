package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/oci"
)

func TestHistoricalMachineOutputV1FixturesRemainDecodable(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "machine-output-v1"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".json" {
			t.Fatalf("unexpected fixture entry %s", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{"build.json", "detect.json", "diff.json", "doctor.json", "pipeline-plan.json", "publish.json", "registry.json", "status.json", "verify.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("fixtures=%v want=%v", names, want)
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", "machine-output-v1", name))
		if err != nil {
			t.Fatal(err)
		}
		document := requireCLIOutputV1(t, raw)
		if len(document) < 2 {
			t.Fatalf("fixture %s has no payload fields", name)
		}
	}
}

func requireCLIOutputV1(t *testing.T, output []byte, required ...string) map[string]json.RawMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	var document map[string]json.RawMessage
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode machine output: %v\n%s", err, output)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		t.Fatalf("machine output has trailing JSON: %s", output)
	}
	var version string
	if err := json.Unmarshal(document["api_version"], &version); err != nil || version != cliOutputAPIVersion {
		t.Fatalf("api_version=%q err=%v output=%s", version, err, output)
	}
	for _, field := range required {
		if _, ok := document[field]; !ok {
			t.Fatalf("missing required field %q: %s", field, output)
		}
	}
	return document
}

func TestCoreMachineOutputsCarryStableV1Envelope(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	layoutDir := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{Binary: binary, Output: layoutDir}); err != nil {
		t.Fatal(err)
	}
	for name, invocation := range map[string]struct {
		args     []string
		required []string
	}{
		"detect":        {[]string{"detect", binary}, []string{"kind", "profile", "evidence"}},
		"verify":        {[]string{"verify", layoutDir}, []string{"valid", "manifests", "blobs", "platforms"}},
		"build dry-run": {[]string{"build", "--dry-run", binary}, []string{"dry_run", "layout", "reference", "platforms", "valid"}},
		"status":        {[]string{"status", "--format", "json", root}, []string{"initialized", "built", "evidence_complete", "published", "deployed", "next_action"}},
		"doctor":        {[]string{"doctor", "--json", "build"}, []string{"ok", "checks"}},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(invocation.args, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			requireCLIOutputV1(t, stdout.Bytes(), invocation.required...)
		})
	}
	var stdout, stderr bytes.Buffer
	if code := runDiff([]string{layoutDir, layoutDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("diff code=%d stderr=%s", code, stderr.String())
	}
	requireCLIOutputV1(t, stdout.Bytes(), "equal", "a", "b")
}

func TestPipelineAndRegistryMachineOutputsCarryStableV1Envelope(t *testing.T) {
	pipelineFile := filepath.Join(t.TempDir(), "pipeline.json")
	if err := os.WriteFile(pipelineFile, []byte(`{"api_version":"platform-factory.dev/v1","name":"demo","stages":[{"id":"build","command":{"executable":"true"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runPipeline([]string{"plan", pipelineFile}, &stdout, &stderr); code != 0 {
		t.Fatalf("pipeline code=%d stderr=%s", code, stderr.String())
	}
	requireCLIOutputV1(t, stdout.Bytes(), "pipeline_api_version", "fingerprint", "order", "levels")

	body := []byte(`{"schemaVersion":2}`)
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	original := inspectRegistryManifest
	inspectRegistryManifest = func(context.Context, registry.Reference, string, string, string, string) ([]byte, string, error) {
		return body, "application/vnd.oci.image.manifest.v1+json", nil
	}
	t.Cleanup(func() { inspectRegistryManifest = original })
	stdout.Reset()
	stderr.Reset()
	if code := runRegistry([]string{"inspect", "registry.example/team/service@" + digest}, &stdout, &stderr); code != 0 {
		t.Fatalf("registry code=%d stderr=%s", code, stderr.String())
	}
	document := requireCLIOutputV1(t, stdout.Bytes(), "reference", "digest", "media_type", "size", "valid")
	if !strings.Contains(string(document["reference"]), "@"+digest) {
		t.Fatalf("reference=%s", document["reference"])
	}
}
