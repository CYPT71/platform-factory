package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/oci"
	"github.com/CYPT71/secure-oci-base/internal/registry"
)

func stubRegistryPush(t *testing.T, digest string, failure error) {
	t.Helper()
	previous := pushOCI
	previousTag := tagOCI
	previousArtifact := pushOCIArtifact
	pushOCI = func(_ context.Context, _ string, target registry.Reference, _, _, _, _, _, _ string) (registry.Result, error) {
		if failure != nil {
			return registry.Result{}, failure
		}
		return registry.Result{
			Digest: digest, Reference: target.Registry + "/" + target.Repository + "@" + digest,
		}, nil
	}
	tagOCI = func(context.Context, string, registry.Reference, string, string, string, string) error {
		return failure
	}
	pushOCIArtifact = func(context.Context, registry.Reference, registry.Result, string, string, []byte, string, string, string) (registry.ArtifactResult, error) {
		return registry.ArtifactResult{Digest: "sha256:" + strings.Repeat("a", 64)}, failure
	}
	t.Cleanup(func() {
		pushOCI = previous
		tagOCI = previousTag
		pushOCIArtifact = previousArtifact
	})
}

func buildPublishLayout(t *testing.T, image, tag string) string {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "service")
	if err := os.WriteFile(binary, []byte("payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "layout")
	if _, err := oci.Build(oci.Options{
		Binary: binary, Output: output, ImageName: image, Tag: tag,
	}); err != nil {
		t.Fatal(err)
	}
	return output
}

func TestRunPublishDryRunIncludesSupplyChainOperations(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{
		"--dry-run", "--allow-incomplete-evidence", "--sign", "--sbom", "--provenance", "provenance.json",
		layoutName, "ghcr.io/example/service:v1",
	}, &stdout, &stderr, func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called during dry-run")
		return nil
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, command := range []string{"native OCI Distribution push", "generate native SBOM", "native DSSE/Ed25519 signature", "publish provenance"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("dry-run missing %q: %s", command, stdout.String())
		}
	}
}

func TestNativePublicationArtifactsRejectInvalidEvidenceInputs(t *testing.T) {
	published := registry.Result{
		Digest:    "sha256:" + strings.Repeat("a", 64),
		Reference: "registry.example/service@sha256:" + strings.Repeat("a", 64),
	}
	artifacts, err := nativePublicationArtifacts("", published, false,
		"", "", "builder", false, "", "")
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%v err=%v", artifacts, err)
	}
	root := t.TempDir()
	invalid := filepath.Join(root, "invalid.json")
	if err := os.WriteFile(invalid, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativePublicationArtifacts("", published, false,
		invalid, "", "builder", false, "", ""); err == nil {
		t.Fatal("invalid provenance accepted")
	}
	if _, err := nativePublicationArtifacts("", published, false,
		filepath.Join(root, "missing"), "", "builder", false, "", ""); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if _, err := nativePublicationArtifacts("", published, false,
		"", invalid, "builder", false, "", ""); err == nil {
		t.Fatal("invalid journal accepted")
	}
	if _, err := nativePublicationArtifacts(filepath.Join(root, "missing-layout"), published, true,
		"", "", "builder", false, "", ""); err == nil {
		t.Fatal("missing SBOM layout accepted")
	}
}

func TestRunPublishExecutesInOrderAndReportsFailure(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	digest := "sha256:" + strings.Repeat("d", 64)
	stubRegistryPush(t, digest, nil)
	var calls []string
	execute := func(name string, args []string, _ io.Reader, stdout, _ io.Writer) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{"--yes", "--allow-incomplete-evidence", "--sign", "--key-dir", t.TempDir(), layoutName, "docker://registry.example/service:v1"}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if len(calls) != 0 {
		t.Fatalf("external commands executed: %v", calls)
	}
	if !strings.Contains(stdout.String(), `"reference": "registry.example/service@sha256:`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	pushOCI = func(context.Context, string, registry.Reference, string, string, string, string, string, string) (registry.Result, error) {
		return registry.Result{}, errors.New("registry unavailable")
	}
	if code := runPublish(context.Background(), []string{"--yes", "--allow-incomplete-evidence", layoutName, "registry.example/service:v1"}, &stdout, &stderr, execute); code != 1 {
		t.Fatalf("failure code=%d", code)
	}
}

func TestRunPublishReferenceOutput(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	digest := "sha256:" + strings.Repeat("e", 64)
	stubRegistryPush(t, digest, nil)
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--format", "reference", layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "registry.example/service@"+digest {
		t.Fatalf("reference=%q", got)
	}
}

func TestPublicationPolicyUsesGeneratedEvidenceAndFailsClosed(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, "policy.json")
	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(rules, []byte(`{
	  "api_version":"secure-oci.dev/policy/v1",
	  "require_pins":true,
	  "require_hardening":true,
	  "require_sbom":true,
	  "require_provenance":true,
	  "require_signature":true,
	  "require_reproducible":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte(`{
	  "subject_digest":"",
	  "sources_pinned":true,
	  "base_pinned":true,
	  "toolchain_pinned":true,
	  "plugins_pinned":true,
	  "non_root":true,
	  "read_only_rootfs":true,
	  "capabilities_dropped":true,
	  "secrets_absent":true,
	  "sbom":false,
	  "provenance":false,
	  "signature":false,
	  "reproducible":true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	decision, err := evaluatePublicationPolicy(rules, evidence, published, true, true, true)
	if err != nil || !decision.Allowed {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	decision, err = evaluatePublicationPolicy(rules, evidence, published, true, false, true)
	if err != nil || decision.Allowed || !strings.Contains(strings.Join(decision.Reasons, " "), "provenance") {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
}

func TestV3PublicationExitCriteriaFailClosed(t *testing.T) {
	t.Run("altered layout never reaches registry", func(t *testing.T) {
		layoutName := buildPublishLayout(t, "example/service", "v1")
		blobs, err := filepath.Glob(filepath.Join(layoutName, "blobs", "sha256", "*"))
		if err != nil || len(blobs) == 0 {
			t.Fatalf("discover blobs: %v (%d found)", err, len(blobs))
		}
		if err := os.Chmod(blobs[0], 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(blobs[0], []byte("altered"), 0o600); err != nil {
			t.Fatal(err)
		}
		pushCalled := false
		previous := pushOCI
		pushOCI = func(context.Context, string, registry.Reference, string, string, string, string, string, string) (registry.Result, error) {
			pushCalled = true
			return registry.Result{}, errors.New("must not be called")
		}
		t.Cleanup(func() { pushOCI = previous })
		var stdout, stderr bytes.Buffer
		if code := runPublish(context.Background(), []string{
			"--yes", "--allow-incomplete-evidence", layoutName, "registry.example/service:v1",
		}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "verify layout") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		if pushCalled {
			t.Fatal("altered layout reached the registry boundary")
		}
	})

	t.Run("unattested production request is rejected", func(t *testing.T) {
		layoutName := buildPublishLayout(t, "example/service", "v1")
		var stdout, stderr bytes.Buffer
		if code := runPublish(context.Background(), []string{
			"--yes", layoutName, "registry.example/service:v1",
		}, &stdout, &stderr, nil); code != 2 || !strings.Contains(stderr.String(), "production publication requires") {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	})

	t.Run("non reproducible evidence denies tag", func(t *testing.T) {
		root := t.TempDir()
		rules := filepath.Join(root, "policy.json")
		evidence := filepath.Join(root, "evidence.json")
		if err := os.WriteFile(rules, []byte(`{
		  "api_version":"secure-oci.dev/policy/v1",
		  "require_reproducible":true
		}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidence, []byte(`{"reproducible":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		decision, err := evaluatePublicationPolicy(rules, evidence,
			registry.Result{Digest: "sha256:" + strings.Repeat("b", 64)}, true, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Allowed || !strings.Contains(strings.Join(decision.Reasons, " "), "reproducibility") {
			t.Fatalf("decision=%+v", decision)
		}
	})
}

// TestPublishDeployRollbackHandleHelpFlag proves all three commands
// recognize -h/--help before any confirmation or validation checks and
// exit cleanly, matching the CLI's documented "COMMAND --help" contract.
func TestPublishDeployRollbackHandleHelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{"--help"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("publish --help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runDeploy(context.Background(), []string{"--help"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("deploy --help code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runRollback([]string{"--help"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("rollback --help code=%d stderr=%s", code, stderr.String())
	}
}

// TestPushOCIAndTagOCIAndArtifactDefaultsFailFast exercises the real
// (non-stubbed) package-level pushOCI, tagOCI and pushOCIArtifact
// implementations. Each is driven with an input that a real
// registry.Client rejects locally (a missing layout, an invalid subject
// digest) before attempting any network I/O, so the assertions are
// deterministic while still proving the default wiring actually
// constructs a *registry.Client and calls through to it.
func TestPushOCIAndTagOCIAndArtifactDefaultsFailFast(t *testing.T) {
	missingLayout := filepath.Join(t.TempDir(), "missing-layout")
	target := registry.Reference{Registry: "registry.example", Repository: "repo", Tag: "v1"}
	if _, err := pushOCI(context.Background(), missingLayout, target, "", "https", "", "", "", ""); err == nil {
		t.Fatal("expected pushOCI to fail for a missing layout before any network call")
	}
	if err := tagOCI(context.Background(), missingLayout, target, "", "https", "", ""); err == nil {
		t.Fatal("expected tagOCI to fail for a missing layout before any network call")
	}
	if _, err := pushOCIArtifact(context.Background(), target, registry.Result{Digest: "not-a-digest"},
		"artifact-type", "payload-type", []byte("x"), "https", "", ""); err == nil {
		t.Fatal("expected pushOCIArtifact to fail for an invalid subject digest before any network call")
	}
}

func TestRunPublishRejectsMutuallyExclusiveProvenanceAndJournal(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--provenance", "a.json", "--journal", "b.json",
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil)
	if code != 2 || !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunPublishRejectsInvalidFormat(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--format", "yaml",
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil)
	if code != 2 || !strings.Contains(stderr.String(), "format must be json or reference") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunPublishRequiresConfirmationEvenWithCompleteEvidenceFlags(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	// No --yes and no --dry-run, but --allow-incomplete-evidence clears the
	// production-evidence gate first: this must still be refused by the
	// separate confirmation check, not accidentally proceed.
	code := runPublish(context.Background(), []string{
		"--allow-incomplete-evidence", layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil)
	if code != 2 || !strings.Contains(stderr.String(), "pass --yes or preview with --dry-run") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunPublishSourceRefAmbiguityAndInvalidImageReference(t *testing.T) {
	root := t.TempDir()
	first := buildPublishLayout(t, "example/first", "v1")
	second := buildPublishLayout(t, "example/second", "v1")
	composed := filepath.Join(root, "multi")
	if _, err := layout.Compose(composed, []string{first, second}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", composed, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 2 || !strings.Contains(stderr.String(), "multiple image references") {
		t.Fatalf("ambiguous source-ref code=%d stderr=%s", code, stderr.String())
	}
	layoutName := buildPublishLayout(t, "example/service", "v1")
	stdout.Reset()
	stderr.Reset()
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", layoutName, "not a valid reference",
	}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("invalid image reference code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunPublishDryRunJournalBranch(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{
		"--dry-run", "--allow-incomplete-evidence", "--journal", "journal.json",
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called during dry-run")
		return nil
	})
	if code != 0 || !strings.Contains(stdout.String(), "generate SLSA provenance from the pipeline journal") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

// TestRunPublishSurfacesArtifactPushFailure isolates a failure of exactly
// one linked-artifact push (not the manifest push itself, which
// stubRegistryPush's shared failure flag cannot isolate) and confirms it
// aborts publication before the tag is ever moved.
func TestRunPublishSurfacesArtifactPushFailure(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	digest := "sha256:" + strings.Repeat("f", 64)
	previousPush, previousTag, previousArtifact := pushOCI, tagOCI, pushOCIArtifact
	pushOCI = func(context.Context, string, registry.Reference, string, string, string, string, string, string) (registry.Result, error) {
		return registry.Result{Digest: digest, Reference: "registry.example/service@" + digest}, nil
	}
	tagOCI = func(context.Context, string, registry.Reference, string, string, string, string) error {
		t.Fatal("tag must not move after an artifact push failure")
		return nil
	}
	pushOCIArtifact = func(context.Context, registry.Reference, registry.Result, string, string, []byte, string, string, string) (registry.ArtifactResult, error) {
		return registry.ArtifactResult{}, errors.New("artifact push refused")
	}
	t.Cleanup(func() { pushOCI, tagOCI, pushOCIArtifact = previousPush, previousTag, previousArtifact })
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--sbom", layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil)
	if code != 1 || !strings.Contains(stderr.String(), "publish SBOM") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestRunPublishAppliesPolicyDecision drives runPublish's own --policy
// handling (not evaluatePublicationPolicy in isolation, which
// TestPublicationPolicyUsesGeneratedEvidenceAndFailsClosed already
// covers): a policy evaluation error and a policy denial must both abort
// before the tag is moved.
func TestRunPublishAppliesPolicyDecision(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	digest := "sha256:" + strings.Repeat("9", 64)
	stubRegistryPush(t, digest, nil)
	root := t.TempDir()
	policyPath := filepath.Join(root, "policy.json")
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(policyPath, []byte(`{"api_version":"secure-oci.dev/policy/v1","require_signature":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--policy", policyPath, "--evidence", evidencePath,
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "policy denied tag update") {
		t.Fatalf("denied code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--policy", policyPath,
		"--evidence", filepath.Join(root, "missing.json"),
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "platform-factory publish: policy:") {
		t.Fatalf("eval error code=%d stderr=%s", code, stderr.String())
	}
}

// TestEvaluatePublicationPolicyDecodeFailures covers the shared decode
// helper's own failure branches: a required-but-empty evidence path, a
// policy file that does not exist, and a policy file containing more than
// one JSON value.
func TestEvaluatePublicationPolicyDecodeFailures(t *testing.T) {
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := evaluatePublicationPolicy("policy.json", "", published, false, false, false); err == nil {
		t.Fatal("expected empty evidence path rejection")
	}
	root := t.TempDir()
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluatePublicationPolicy(filepath.Join(root, "missing-policy.json"), evidencePath,
		published, false, false, false); err == nil {
		t.Fatal("expected missing policy file rejection")
	}
	trailing := filepath.Join(root, "trailing.json")
	if err := os.WriteFile(trailing, []byte(`{"api_version":"secure-oci.dev/policy/v1"}{"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluatePublicationPolicy(trailing, evidencePath, published, false, false, false); err == nil {
		t.Fatal("expected trailing-content policy file rejection")
	}
}

// TestNativePublicationArtifactsSignUsesDefaultKeyDirectory drives the
// keyDir=="" branch, which resolves the signing key directory from the
// user's home directory rather than an explicit --key-dir.
func TestNativePublicationArtifactsSignUsesDefaultKeyDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	published := registry.Result{
		Digest:    "sha256:" + strings.Repeat("a", 64),
		Reference: "registry.example/service@sha256:" + strings.Repeat("a", 64),
	}
	artifacts, err := nativePublicationArtifacts("", published, false, "", "", "builder", true, "", "release")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].name != "signature" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}

func TestNativePublicationArtifactsSignSurfacesKeyStoreFailure(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := nativePublicationArtifacts("", published, false, "", "", "builder", true, blocked, "release"); err == nil {
		t.Fatal("expected key store failure when key-dir is a regular file")
	}
}

func TestNativePublicationArtifactsSignSurfacesPublicKeyFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := nativePublicationArtifacts("", published, false, "", "", "builder", true, dir, "release"); err == nil {
		t.Fatal("expected public key generation failure when key-dir is unwritable")
	}
}

// TestNativePublicationArtifactsJournalProvenance covers the
// journal-derived provenance path (as opposed to the --provenance file
// path already covered indirectly through launch_publish_test.go), both
// on success and when the journal file cannot be opened.
func TestNativePublicationArtifactsJournalProvenance(t *testing.T) {
	root := t.TempDir()
	journalPath := filepath.Join(root, "journal.json")
	journal := `{
	  "api_version":"secure-oci.dev/journal/v1",
	  "pipeline_fingerprint":"sha256:abc",
	  "engine_version":"platform-factory/1",
	  "sandbox":"on",
	  "generated":"2026-07-28T12:00:00Z",
	  "stages":[{"id":"build","state":"succeeded"}]
	}`
	if err := os.WriteFile(journalPath, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	published := registry.Result{Digest: "sha256:" + strings.Repeat("a", 64)}
	artifacts, err := nativePublicationArtifacts("", published, false, "", journalPath,
		"https://secure-oci.dev/builder/v1", false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].name != "provenance" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	if _, err := nativePublicationArtifacts("", published, false, "", filepath.Join(root, "missing.json"),
		"builder", false, "", ""); err == nil {
		t.Fatal("expected missing journal file to fail")
	}
}

func TestRunDeployDryRunEmitsHardenedManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeploy(context.Background(), []string{
		"--dry-run", "--name", "api", "--namespace", "production",
		"--replicas", "3", "--port", "8443",
		"ghcr.io/example/api@sha256:" + strings.Repeat("a", 64),
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, value := range []string{`"runAsNonRoot": true`, `"readOnlyRootFilesystem": true`, `"allowPrivilegeEscalation": false`, `"ALL"`} {
		if !strings.Contains(output, value) {
			t.Fatalf("manifest missing %s: %s", value, output)
		}
	}
	if code := runDeploy(context.Background(), []string{"example/api:latest"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("mutable tag code=%d", code)
	}
}

func TestRunDeployAndRollbackCommands(t *testing.T) {
	var calls [][]string
	execute := func(name string, args []string, stdin io.Reader, _, _ io.Writer) error {
		call := append([]string{name}, args...)
		calls = append(calls, call)
		if args[0] == "apply" && stdin == nil {
			t.Fatal("apply has no manifest")
		}
		return nil
	}
	var stdout, stderr bytes.Buffer
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("b", 64)
	if code := runDeploy(context.Background(), []string{"--yes", "--name", "api", "--namespace", "prod", image}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("deploy code=%d stderr=%s", code, stderr.String())
	}
	if code := runRollback([]string{"--yes", "--namespace", "prod", "--to-revision", "2", "api"}, &stdout, &stderr, execute); code != 0 {
		t.Fatalf("rollback code=%d stderr=%s", code, stderr.String())
	}
	want := [][]string{
		{"kubectl", "apply", "-f", "-"},
		{"kubectl", "rollout", "status", "deployment/api", "--namespace", "prod", "--timeout", "2m"},
		{"kubectl", "rollout", "undo", "deployment/api", "--namespace", "prod", "--to-revision=2"},
		{"kubectl", "rollout", "status", "deployment/api", "--namespace", "prod", "--timeout", "2m"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

func TestLifecycleRequiresConfirmationAndValidNames(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{layoutName, "registry.example/service:v1"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("unconfirmed publish code=%d", code)
	}
	stderr.Reset()
	if code := runPublish(context.Background(), []string{"--yes", layoutName, "registry.example/service:v1"}, &stdout, &stderr, nil); code != 2 ||
		!strings.Contains(stderr.String(), "production publication requires") {
		t.Fatalf("incomplete evidence code=%d stderr=%s", code, stderr.String())
	}
	image := "registry.example/service@sha256:" + strings.Repeat("a", 64)
	if code := runDeploy(context.Background(), []string{"--name", "Invalid_Name", image}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("invalid deployment name code=%d", code)
	}
	if code := runRollback([]string{"api"}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("unconfirmed rollback code=%d", code)
	}
	for _, invalid := range []string{"service:latest", "service@sha256:short", "service@sha256:" + strings.Repeat("g", 64)} {
		if validDigestReference(invalid) {
			t.Fatalf("accepted digest reference %q", invalid)
		}
	}
	for _, invalid := range []string{"", "-api", "api-", "API", "api_name", strings.Repeat("a", 64)} {
		if validKubernetesName(invalid) {
			t.Fatalf("accepted Kubernetes name %q", invalid)
		}
	}
}

func TestCompletionAndCommandFormatting(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		var stdout, stderr bytes.Buffer
		if code := runCompletion([]string{shell}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "platform-factory") {
			t.Fatalf("shell=%s code=%d stdout=%s stderr=%s", shell, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runCompletion([]string{"unknown"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown shell code=%d", code)
	}
	if code := runCompletion(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing shell code=%d", code)
	}
	if got := formatCommand("tool", []string{"plain", "two words", "it's"}); got != `tool plain 'two words' 'it'\''s'` {
		t.Fatalf("formatted command=%q", got)
	}
}

type lifecycleExitError struct{ code int }

func (errorValue lifecycleExitError) Error() string { return "command failed" }
func (errorValue lifecycleExitError) ExitCode() int { return errorValue.code }

func TestLifecycleDryRunsErrorsAndDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runRollback([]string{"--dry-run", "--to-revision", "3", "api"}, &stdout, &stderr, nil); code != 0 ||
		!strings.Contains(stdout.String(), "--to-revision=3") {
		t.Fatalf("rollback dry-run code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	failing := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		return lifecycleExitError{code: 17}
	}
	image := "registry.example/api@sha256:" + strings.Repeat("c", 64)
	if code := runDeploy(context.Background(), []string{"--yes", image}, &stdout, &stderr, failing); code != 17 {
		t.Fatalf("exit code=%d", code)
	}
	if code := runDeploy(context.Background(), []string{image}, &stdout, &stderr, failing); code != 2 {
		t.Fatalf("deploy without --yes code=%d", code)
	}
	for _, args := range [][]string{
		{"completion", "bash"},
		{"deploy", "--dry-run", image},
		{"rollback", "--dry-run", "api"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("dispatch args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	for _, call := range []func() int{
		func() int { return runPublish(context.Background(), nil, &stdout, &stderr, nil) },
		func() int { return runDeploy(context.Background(), nil, &stdout, &stderr, nil) },
		func() int { return runRollback(nil, &stdout, &stderr, nil) },
	} {
		if code := call(); code != 2 {
			t.Fatalf("missing arguments code=%d", code)
		}
	}
}

func TestDefaultUploadSessionDirHonorsExplicitConfiguration(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_UPLOAD_SESSION_DIR", "  /tmp/platform-factory-uploads  ")
	if got := defaultUploadSessionDir(); got != "/tmp/platform-factory-uploads" {
		t.Fatalf("session directory=%q", got)
	}
}
