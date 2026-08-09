package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/policy"
	"github.com/CYPT71/secure-oci-base/internal/project"
	"github.com/CYPT71/secure-oci-base/internal/registry"
)

func TestLaunchPublishCompletesNativeProductionLifecycle(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: compiled\nartifact: app\nimage: registry.example/team/app\ntag: v1\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "native payload", 0o755)
	digest := "sha256:" + strings.Repeat("d", 64)
	stubRegistryPush(t, digest, nil)

	artifactCount := 0
	previousArtifact := pushOCIArtifact
	pushOCIArtifact = func(context.Context, registry.Reference, registry.Result, string, string, []byte, string, string, string) (registry.ArtifactResult, error) {
		artifactCount++
		return registry.ArtifactResult{Digest: "sha256:" + strings.Repeat("a", 64)}, nil
	}
	t.Cleanup(func() { pushOCIArtifact = previousArtifact })

	var runtimeCalls int
	containerExecute := func(_ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if len(args) > 0 && args[0] == "run" {
			runtimeCalls++
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	code := runLaunchPublish([]string{
		"--publish", "--yes", "--key-dir", filepath.Join(root, "keys"), root,
	}, &stdout, &stderr,
		func(string, []string, string, io.Writer, io.Writer) error { return nil },
		containerExecute, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if artifactCount != 3 {
		t.Fatalf("published artifacts=%d, want SBOM+provenance+signature", artifactCount)
	}
	if runtimeCalls != 1 {
		t.Fatalf("runtime calls=%d, want 1", runtimeCalls)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not one JSON result: %v\n%s", err, stdout.String())
	}
	if result["published"] != true || result["reproducible"] != true ||
		result["published_reference"] != "registry.example/team/app@"+digest {
		t.Fatalf("result=%+v", result)
	}
	for _, name := range []string{"policy.json", "evidence.json", "provenance.json"} {
		info, err := os.Stat(filepath.Join(root, ".platform-factory", "publication", name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o", name, info.Mode().Perm())
		}
	}
	var rules policy.Rules
	data, err := os.ReadFile(filepath.Join(root, ".platform-factory", "publication", "policy.json"))
	if err != nil || json.Unmarshal(data, &rules) != nil {
		t.Fatalf("read policy: %v", err)
	}
	if !rules.RequireHardening || !rules.RequireSBOM || !rules.RequireProvenance ||
		!rules.RequireSignature || !rules.RequireReproducible {
		t.Fatalf("incomplete production policy: %+v", rules)
	}
}

func TestLaunchPublishRejectsNonReproducibleBuildBeforeRegistry(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: compiled\nartifact: app\nimage: registry.example/team/app\ntag: v1\nbuild_command: [build]\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "initial", 0o755)
	builds := 0
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		builds++
		return os.WriteFile(filepath.Join(root, "app"), []byte(strings.Repeat("x", builds)), 0o755)
	}
	pushCalled := false
	previousPush := pushOCI
	pushOCI = func(context.Context, string, registry.Reference, string, string, string, string, string, string) (registry.Result, error) {
		pushCalled = true
		return registry.Result{}, nil
	}
	t.Cleanup(func() { pushOCI = previousPush })

	var stdout, stderr bytes.Buffer
	code := runLaunchPublish([]string{
		"--publish", "--yes", "--key-dir", filepath.Join(root, "keys"), root,
	}, &stdout, &stderr, execute, nil, nil)
	if code != 1 || !strings.Contains(stderr.String(), "reproducibility check failed") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if pushCalled {
		t.Fatal("non-reproducible build reached registry")
	}
}

func TestLaunchPublishRequiresConfirmationAndQualifiedTarget(t *testing.T) {
	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: compiled\nartifact: app\nimage: local-app\ntag: v1\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "payload", 0o755)
	var stdout, stderr bytes.Buffer
	if code := runLaunchPublish([]string{"--publish", root}, &stdout, &stderr, nil, nil, nil); code != 2 ||
		!strings.Contains(stderr.String(), "pass --yes") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := runLaunchPublish([]string{"--publish", "--yes", root}, &stdout, &stderr, nil, nil, nil); code != 2 ||
		!strings.Contains(stderr.String(), "explicit registry") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestLaunchPublishRoutingDoesNotCapturePortForwarding(t *testing.T) {
	if !hasLaunchPublishFlag([]string{"--publish", "--yes"}) ||
		!hasLaunchPublishFlag([]string{"--publish=true", "--yes"}) {
		t.Fatal("registry publication flag was not detected")
	}
	for _, args := range [][]string{
		{"--publish=8080:80", "--isolation=container", "image"},
		{"--publish=127.0.0.1:8443:443/tcp", "image"},
	} {
		if hasLaunchPublishFlag(args) {
			t.Fatalf("port forwarding was mistaken for registry publication: %v", args)
		}
	}
}

func TestLaunchPublishReportsInvalidInputsAndBuildFailures(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runLaunchPublish([]string{"--help"}, &stdout, &stderr, nil, nil, nil); code != 0 {
		t.Fatalf("help code=%d", code)
	}
	stderr.Reset()
	if code := runLaunchPublish([]string{"--publish", "--yes", t.TempDir(), "extra"}, &stdout, &stderr, nil, nil, nil); code != 2 {
		t.Fatalf("extra argument code=%d", code)
	}
	stderr.Reset()
	if code := runLaunchPublish([]string{"--publish", "--yes", filepath.Join(t.TempDir(), "missing")},
		&stdout, &stderr, nil, nil, nil); code != 1 || !strings.Contains(stderr.String(), "no project image config") {
		t.Fatalf("missing config code=%d stderr=%s", code, stderr.String())
	}

	root := t.TempDir()
	writeProjectTestFile(t, filepath.Join(root, ".config_image.yaml"),
		"language: compiled\nartifact: app\nimage: registry.example/team/app\ntag: v1\nbuild_command: [compile]\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "payload", 0o755)
	stderr.Reset()
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		return errors.New("compiler failed")
	}
	if code := runLaunchPublish([]string{"--publish", "--yes", root},
		&stdout, &stderr, execute, nil, nil); code != 1 || !strings.Contains(stderr.String(), "compiler failed") {
		t.Fatalf("build failure code=%d stderr=%s", code, stderr.String())
	}
}

func TestLaunchPublicationJSONFailsClosed(t *testing.T) {
	root := t.TempDir()
	parentFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeLaunchJSON(filepath.Join(parentFile, "evidence.json"), map[string]bool{"valid": true}); err == nil {
		t.Fatal("write through a non-directory parent succeeded")
	}
}

func TestReproducibleProjectBuildRestoresPreviousLayout(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, ".config_image.yaml")
	writeProjectTestFile(t, config,
		"language: compiled\nartifact: app\nimage: registry.example/team/app\ntag: v1\nbuild_command: [compile]\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "initial", 0o755)
	loaded, err := project.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(loaded.Output(), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(loaded.Output(), "previous")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	builds := 0
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		builds++
		return os.WriteFile(filepath.Join(root, "app"), []byte(strings.Repeat("x", builds)), 0o755)
	}
	var stdout, stderr bytes.Buffer
	first, second, code := reproducibleProjectBuild(loaded, &stdout, &stderr, execute)
	if code != 0 || first == second {
		t.Fatalf("first=%s second=%s code=%d stderr=%s", first, second, code, stderr.String())
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("previous layout was not restored: %q err=%v", data, err)
	}
}

func TestReproducibleProjectBuildRestoresFirstCandidateAfterSecondFailure(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, ".config_image.yaml")
	writeProjectTestFile(t, config,
		"language: compiled\nartifact: app\nimage: registry.example/team/app\ntag: v1\nbuild_command: [compile]\n", 0o600)
	writeProjectTestFile(t, filepath.Join(root, "app"), "initial", 0o755)
	loaded, err := project.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	execute := func(string, []string, string, io.Writer, io.Writer) error {
		builds++
		if builds == 2 {
			return errors.New("second build failed")
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	first, second, code := reproducibleProjectBuild(loaded, &stdout, &stderr, execute)
	if code != 1 || first == "" || second != "" {
		t.Fatalf("first=%s second=%s code=%d stderr=%s", first, second, code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(loaded.Output(), "index.json")); err != nil {
		t.Fatalf("first candidate was not restored: %v", err)
	}
}
