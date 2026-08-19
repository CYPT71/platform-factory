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

	"github.com/CYPT71/platform-factory/internal/app/publish"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/idempotency"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/workloadstate"
	"github.com/CYPT71/platform-factory/oci"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

// freshOperationJournal points operationJournalFor (and, since runPublish
// now also drives the state machine, workloadStateStoreFor) at brand new,
// empty t.TempDir()-backed stores, restored by t.Cleanup. Every test that
// drives runPublish/runDeploy/runRollback past their --yes/--dry-run gate
// needs this: without it, they'd share the real default roots
// (defaultOperationJournalRoot/defaultWorkloadStateRoot, under the
// developer/CI machine's actual cache directory), so a claim one test run
// leaves Completed or Failed - or a workload state one test run leaves
// Published - would make the identical operation identity - the same
// target/namespace/name/image, which several fixtures here deliberately
// reuse - look already-done, or already-published-once-and-therefore-
// illegal-to-re-Publish, on the next `go test` run, or even to a later
// test in the same run. Call it again mid-test before any call meant to be
// treated as an independent, unrelated CLI invocation rather than a retry
// of the one before it.
func freshOperationJournal(t *testing.T) {
	t.Helper()
	journalRoot := t.TempDir()
	previousJournal := operationJournalFor
	operationJournalFor = func() (core.OperationJournal, error) {
		return idempotency.NewFileJournal(journalRoot)
	}
	t.Cleanup(func() { operationJournalFor = previousJournal })

	stateRoot := t.TempDir()
	previousState := workloadStateStoreFor
	workloadStateStoreFor = func() (workloadstate.Store, error) {
		return workloadstate.NewFileStore(stateRoot)
	}
	t.Cleanup(func() { workloadStateStoreFor = previousState })
}

func stubRegistryPush(t *testing.T, digest string, failure error) {
	t.Helper()
	freshOperationJournal(t)
	previous := pushOCI
	previousTag := tagOCI
	previousArtifact := pushOCIArtifact
	previousVerify := verifyRemoteDigest
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
	verifyRemoteDigest = func(context.Context, string, string, string, string, string) error { return failure }
	t.Cleanup(func() {
		pushOCI = previous
		tagOCI = previousTag
		pushOCIArtifact = previousArtifact
		verifyRemoteDigest = previousVerify
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
		"--dry-run", "--push-only", "--allow-incomplete-evidence", "--sign", "--sbom", "--provenance", "provenance.json",
		layoutName, "ghcr.io/example/service:v1",
	}, &stdout, &stderr, func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("executor called during dry-run")
		return nil
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, command := range []string{"native OCI Distribution push", "generate native SBOM", "native DSSE/Ed25519 signature", "publish provenance", "move tag v1 only after"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("dry-run missing %q: %s", command, stdout.String())
		}
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
	// A fresh journal: this second call is meant to prove pushOCI failure
	// propagates, independent of the first call already having completed
	// the exact same operation identity (same layout, same target).
	freshOperationJournal(t)
	if code := runPublish(context.Background(), []string{"--yes", "--allow-incomplete-evidence", layoutName, "registry.example/service:v1"}, &stdout, &stderr, execute); code != 1 {
		t.Fatalf("failure code=%d", code)
	}
}

// TestRunPublishTransitionsWorkloadStateThroughStateMachine proves runPublish
// actually drives internal/core.RuntimeState.TransitionTo (statemachine.go),
// not just the operation journal - the item mvp.md's own §4 flagged as
// deferred twice ("Actually wire the state machine in product paths").
// A successful publish must leave the workload at PhasePublished; a failed
// one must leave it at PhaseFailed - both reached only through the
// legal Built -> Publishing -> {Published,Failed} path the transition
// table (statemachine.go) defines, since transitionPublishWorkload treats
// an unrecorded workload as starting from PhaseBuilt.
func TestRunPublishTransitionsWorkloadStateThroughStateMachine(t *testing.T) {
	digest := "sha256:" + strings.Repeat("f", 64)

	t.Run("success reaches PhasePublished", func(t *testing.T) {
		layoutName := buildPublishLayout(t, "example/service", "v1")
		stubRegistryPush(t, digest, nil)
		var stdout, stderr bytes.Buffer
		if code := runPublish(context.Background(), []string{
			"--yes", "--allow-incomplete-evidence", layoutName, "registry.example/success:v1",
		}, &stdout, &stderr, nil); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
		store, err := workloadStateStoreFor()
		if err != nil {
			t.Fatal(err)
		}
		state, ok, err := store.Lookup(cliWorkloadID("publish", "registry.example", "success", "v1"))
		if err != nil || !ok || state.Phase != core.PhasePublished {
			t.Fatalf("state=%+v ok=%v err=%v, want PhasePublished", state, ok, err)
		}
	})

	t.Run("failure reaches PhaseFailed", func(t *testing.T) {
		layoutName := buildPublishLayout(t, "example/service", "v1")
		stubRegistryPush(t, digest, errors.New("registry unavailable"))
		var stdout, stderr bytes.Buffer
		if code := runPublish(context.Background(), []string{
			"--yes", "--allow-incomplete-evidence", layoutName, "registry.example/failure:v1",
		}, &stdout, &stderr, nil); code == 0 {
			t.Fatalf("expected failure, code=%d stderr=%s", code, stderr.String())
		}
		store, err := workloadStateStoreFor()
		if err != nil {
			t.Fatal(err)
		}
		state, ok, err := store.Lookup(cliWorkloadID("publish", "registry.example", "failure", "v1"))
		if err != nil || !ok || state.Phase != core.PhaseFailed {
			t.Fatalf("state=%+v ok=%v err=%v, want PhaseFailed", state, ok, err)
		}
	})
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
		}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "verify project layout") {
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
		  "api_version":"platform-factory.dev/policy/v1",
		  "require_reproducible":true
		}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidence, []byte(`{"reproducible":false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		decision, err := publish.EvaluatePolicy(rules, evidence,
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
	if code := runRollback(context.Background(), []string{"--help"}, &stdout, &stderr, nil); code != 0 {
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
	freshOperationJournal(t)
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
// before any registry upload.
func TestRunPublishAppliesPolicyDecision(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	digest := "sha256:" + strings.Repeat("9", 64)
	stubRegistryPush(t, digest, nil)
	root := t.TempDir()
	policyPath := filepath.Join(root, "policy.json")
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(policyPath, []byte(`{"api_version":"platform-factory.dev/policy/v1","require_signature":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--policy", policyPath, "--evidence", evidencePath,
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "policy denied publication before upload") {
		t.Fatalf("denied code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	// A fresh journal: this second call exercises a distinct, independent
	// failure (a policy evaluation error), not a retry of the denial above.
	freshOperationJournal(t)
	if code := runPublish(context.Background(), []string{
		"--yes", "--allow-incomplete-evidence", "--policy", policyPath,
		"--evidence", filepath.Join(root, "missing.json"),
		layoutName, "registry.example/service:v1",
	}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stderr.String(), "platform-factory publish: policy preflight:") {
		t.Fatalf("eval error code=%d stderr=%s", code, stderr.String())
	}
}

// TestEvaluatePublicationPolicyDecodeFailures covers the shared decode
// helper's own failure branches: a required-but-empty evidence path, a
// policy file that does not exist, and a policy file containing more than
// one JSON value.

// TestNativePublicationArtifactsSignUsesDefaultKeyDirectory drives the
// keyDir=="" branch, which resolves the signing key directory from the
// user's home directory rather than an explicit --key-dir.

// TestNativePublicationArtifactsJournalProvenance covers the
// journal-derived provenance path (as opposed to the --provenance file
// path already covered indirectly through launch_publish_test.go), both
// on success and when the journal file cannot be opened.

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

func TestRunDeployRuntimeClassAddsCompatibleNodeScheduling(t *testing.T) {
	var stdout, stderr bytes.Buffer
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("e", 64)
	code := runDeploy(context.Background(), []string{
		"--dry-run", "--runtime-class", "platform-factory", image,
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"runtimeClassName": "platform-factory"`, `"platform-factory.dev/runtime-platform-factory": "ready"`, `"effect": "NoSchedule"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("manifest missing %s: %s", want, stdout.String())
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runDeploy(context.Background(), []string{"--dry-run", "--runtime-class", "../host", image}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("hostile runtime class accepted: code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunDeployGeneratesAdditionalWorkloads(t *testing.T) {
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("f", 64)
	for _, test := range []struct {
		args []string
		kind string
	}{
		{[]string{"--dry-run", "--workload", "statefulset", image}, `"kind": "StatefulSet"`},
		{[]string{"--dry-run", "--workload", "daemonset", image}, `"kind": "DaemonSet"`},
		{[]string{"--dry-run", "--workload", "cronjob", "--schedule", "*/5 * * * *", image}, `"kind": "CronJob"`},
	} {
		var stdout, stderr bytes.Buffer
		if code := runDeploy(context.Background(), test.args, &stdout, &stderr, nil); code != 0 {
			t.Fatalf("args=%v code=%d stderr=%s", test.args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), test.kind) {
			t.Fatalf("args=%v missing %s: %s", test.args, test.kind, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runDeploy(context.Background(), []string{"--dry-run", "--workload", "cronjob", image}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("cronjob without schedule accepted: code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunDeployGeneratesIngressConfigSecretReferencesAndPVCWithoutSecretValues(t *testing.T) {
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)
	var stdout, stderr bytes.Buffer
	args := []string{"--dry-run", "--ingress-host", "api.example.com", "--ingress-path", "/v1", "--config", "MODE=production", "--secret-env", "DATABASE_PASSWORD=database/password", "--volume", "/var/lib/api=20Gi", image}
	if code := runDeploy(context.Background(), args, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"kind": "Ingress"`, `"kind": "ConfigMap"`, `"kind": "PersistentVolumeClaim"`, `"secretKeyRef"`, `"name": "database"`, `"key": "password"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("manifest missing %s: %s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "DATABASE_PASSWORD=") {
		t.Fatalf("manifest exposed secret syntax: %s", stdout.String())
	}
}

func TestRunDeployRejectsMalformedKubernetesExtensionsBeforeKubectl(t *testing.T) {
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("b", 64)
	for _, args := range [][]string{
		{"--dry-run", "--config", "MISSING", image},
		{"--dry-run", "--secret-env", "TOKEN=secret", image},
		{"--dry-run", "--volume", "relative=1Gi", image},
		{"--dry-run", "--ingress-host", "Bad_Host", image},
	} {
		var stdout, stderr bytes.Buffer
		if code := runDeploy(context.Background(), args, &stdout, &stderr, nil); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestRunDeployServiceWorkloadIncludesAMatchingService(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeploy(context.Background(), []string{
		"--dry-run", "--workload", "service", "--name", "api", "--port", "8443",
		"ghcr.io/example/api@sha256:" + strings.Repeat("a", 64),
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var list struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Kind != "List" || len(list.Items) != 2 {
		t.Fatalf("kind=%s items=%d, want List of 2", list.Kind, len(list.Items))
	}
	var deployment, service map[string]any
	if err := json.Unmarshal(list.Items[0], &deployment); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(list.Items[1], &service); err != nil {
		t.Fatal(err)
	}
	if deployment["kind"] != "Deployment" || service["kind"] != "Service" {
		t.Fatalf("kinds=%v/%v, want Deployment/Service", deployment["kind"], service["kind"])
	}
	spec, _ := service["spec"].(map[string]any)
	selector, _ := spec["selector"].(map[string]any)
	if selector["app.kubernetes.io/name"] != "api" {
		t.Fatalf("service selector=%v, want to match the Deployment's pod label", selector)
	}
}

func TestRunDeployDeploymentHasTCPProbesOnTheContainerPort(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeploy(context.Background(), []string{
		"--dry-run", "--workload", "service", "--port", "9090",
		"ghcr.io/example/api@sha256:" + strings.Repeat("a", 64),
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{`"readinessProbe"`, `"livenessProbe"`, `"tcpSocket"`, `"port": 9090`} {
		if !strings.Contains(output, want) {
			t.Fatalf("manifest missing %s: %s", want, output)
		}
	}
}

// TestRunDeployAppliesPolicyDecision mirrors
// TestRunPublishAppliesPolicyDecision: a denied or unevaluable policy must
// block kubectl apply from ever running.
func TestRunDeployAppliesPolicyDecision(t *testing.T) {
	root := t.TempDir()
	policyPath := filepath.Join(root, "policy.json")
	evidencePath := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(policyPath, []byte(`{"api_version":"platform-factory.dev/policy/v1","require_signature":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("kubectl invoked despite a denied policy")
		return nil
	}
	var stdout, stderr bytes.Buffer
	if code := runDeploy(context.Background(), []string{
		"--yes", "--policy", policyPath, "--evidence", evidencePath, image,
	}, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "policy denied deployment") {
		t.Fatalf("denied code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := runDeploy(context.Background(), []string{
		"--yes", "--policy", policyPath, "--evidence", filepath.Join(root, "missing.json"), image,
	}, &stdout, &stderr, execute); code != 1 || !strings.Contains(stderr.String(), "platform-factory deploy: policy:") {
		t.Fatalf("eval error code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunDeployResourceRequests(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDeploy(context.Background(), []string{
		"--dry-run", "--workload", "job", "--cpu-request", "250m", "--memory-request", "256Mi",
		"ghcr.io/example/api@sha256:" + strings.Repeat("a", 64),
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{`"requests"`, `"cpu": "250m"`, `"memory": "256Mi"`} {
		if !strings.Contains(output, want) {
			t.Fatalf("manifest missing %s: %s", want, output)
		}
	}
	if code := runDeploy(context.Background(), []string{
		"--dry-run", "--cpu-request", "", "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64),
	}, &stdout, &stderr, nil); code != 2 {
		t.Fatalf("empty --cpu-request accepted: code=%d", code)
	}
}

func TestRunDeploySelectsJobForInitializedProjectWithoutPorts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte("version: 1\nlanguage: python\nprofile: python\nartifact: app.py\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	image := "registry.example/hello@sha256:" + strings.Repeat("d", 64)
	var stdout, stderr bytes.Buffer
	if code := runDeploy(context.Background(), []string{"--dry-run", image}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "Job"`) || strings.Contains(stdout.String(), "containerPort") {
		t.Fatalf("one-shot project got the wrong manifest: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "project declares no listening ports") {
		t.Fatalf("selection was not explained: %s", stderr.String())
	}
}

func TestRunPublishDiscoversInitializedProjectLayout(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/hello", "v1")
	root := t.TempDir()
	relative, err := filepath.Rel(root, layoutName)
	if err != nil {
		t.Fatal(err)
	}
	config := "version: 1\nlanguage: compiled\nprofile: static\nartifact: app\noutput: " + relative + "\n"
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{"--dry-run", "--allow-incomplete-evidence", "registry.example/hello:v1"}, &stdout, &stderr, nil)
	if code != 0 || !strings.Contains(stdout.String(), layoutName) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestRunPublishDiscoversCompleteProjectReleaseBundle(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/hello", "v1")
	root := t.TempDir()
	relative, err := filepath.Rel(root, layoutName)
	if err != nil {
		t.Fatal(err)
	}
	config := "version: 1\nlanguage: compiled\nprofile: static\nartifact: app\noutput: " + relative + "\n"
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	releaseDir := filepath.Join(root, ".platform-factory", "release")
	reportsDir := filepath.Join(releaseDir, "reports")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(releaseDir, "sbom.json"), map[string]any{"components": []any{}})
	writeJSONFile(t, filepath.Join(releaseDir, "provenance.json"), map[string]any{"subject_digest": "sha256:" + strings.Repeat("a", 64)})
	writeJSONFile(t, filepath.Join(reportsDir, "policy-rules.json"), policy.Rules{
		APIVersion: policy.APIVersion, RequireSBOM: true, RequireProvenance: true,
		RequireSignature: true, RequireReproducible: true,
	})
	writeJSONFile(t, filepath.Join(reportsDir, "evidence.json"), policy.Evidence{
		SBOM: true, Provenance: true, Signature: true, Reproducible: true,
	})
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := runPublish(context.Background(), []string{"--dry-run", "registry.example/hello:v1"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"policy preflight allowed", "generate native SBOM", "DSSE/Ed25519 signature", "publish provenance",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("dry-run missing %q: %s", want, stdout.String())
		}
	}
	report, err := layout.Verify(layoutName)
	if err != nil {
		t.Fatal(err)
	}
	stubRegistryPush(t, report.Platforms[0].Digest, nil)
	stdout.Reset()
	stderr.Reset()
	code = runPublish(context.Background(), []string{
		"--yes", "--key-dir", filepath.Join(root, "keys"), "registry.example/hello:v1",
	}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("publish code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	publishedData, err := os.ReadFile(filepath.Join(root, ".platform-factory", "published.json"))
	if err != nil {
		t.Fatal(err)
	}
	var published struct {
		Reference string `json:"reference"`
		Digest    string `json:"digest"`
	}
	if err := json.Unmarshal(publishedData, &published); err != nil {
		t.Fatal(err)
	}
	if published.Digest != report.Platforms[0].Digest || !strings.Contains(published.Reference, "@"+published.Digest) {
		t.Fatalf("published state=%s", publishedData)
	}
	publicationMetrics, err := os.ReadFile(filepath.Join(root, ".platform-factory", "publication", "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"operation": "publish"`, `"artifacts": 3`, `"tag_moved": true`, published.Digest} {
		if !strings.Contains(string(publicationMetrics), want) {
			t.Fatalf("publication metrics missing %s: %s", want, publicationMetrics)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runDeploy(context.Background(), []string{"--dry-run"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("deploy from publication code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), published.Reference) {
		t.Fatalf("deploy did not consume persisted digest: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPublish(context.Background(), []string{"--deploy-only", "--dry-run"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("publish --deploy-only code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), published.Reference) {
		t.Fatalf("--deploy-only did not consume persisted digest: %s", stdout.String())
	}
	workingRemoteVerify := verifyRemoteDigest
	verifyRemoteDigest = func(context.Context, string, string, string, string, string) error {
		return errors.New("manifest not found")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runPublish(context.Background(), []string{"--deploy-only", "--dry-run"}, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "published digest is not verifiable") {
		t.Fatalf("missing remote digest code=%d stderr=%s", code, stderr.String())
	}
	verifyRemoteDigest = workingRemoteVerify
}

func TestRunPublishRejectsIncompleteAutomaticReleaseBundle(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/hello", "v1")
	root := t.TempDir()
	relative, err := filepath.Rel(root, layoutName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pf.yaml"), []byte(
		"version: 1\nlanguage: compiled\nprofile: static\nartifact: app\noutput: "+relative+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{"--dry-run", "registry.example/hello:v1"}, &stdout, &stderr, nil); code != 1 ||
		!strings.Contains(stderr.String(), "release bundle is incomplete or unsafe") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// TestDeployToClusterAppliesThenObservesPerWorkload drives
// deployToCluster directly against a stub deployment plugin - the same
// separation TestDispatchKubeVirtRoutesMutatingActionsThroughIdempotency
// (main_test.go) already uses for dispatchKubeVirt - covering what a
// real (non-dry-run) `pf deploy` cannot exercise without a live cluster:
// apply is a mutating CallWithIdempotency (crash-safe replay), the
// post-apply observation is a plain read-only Call, and which
// observation Kind/ResourceType gets requested depends on
// selectedWorkload exactly the way the old kubectl
// wait/get-cronjob/rollout-status choice did.
func TestDeployToClusterAppliesThenObservesPerWorkload(t *testing.T) {
	for _, test := range []struct {
		workload         string
		wantKind         string
		wantResourceType string
	}{
		{"job", "wait-job", ""},
		{"cronjob", "get-cronjob", ""},
		{"service", "rollout-status", "deployment"},
		{"statefulset", "rollout-status", "statefulset"},
		{"daemonset", "rollout-status", "daemonset"},
	} {
		stub := &stubDeploymentPlugin{
			capabilities:  allDeploymentCapabilities(),
			observeResult: api.DeploymentObserveResult{Output: "ready", Ready: true},
		}
		host := &pluginHost{clients: []pluginClient{stub}}
		manifest := []byte(`{"kind":"Deployment"}`)
		output, err := deployToCluster(context.Background(), host, manifest, test.workload, "api", "prod", "ghcr.io/example/api@sha256:"+strings.Repeat("b", 64), "2m")
		if err != nil {
			t.Fatalf("workload=%s err=%v", test.workload, err)
		}
		if output != "ready" {
			t.Fatalf("workload=%s output=%q", test.workload, output)
		}
		wantCalls := []string{
			"idempotent:v1." + api.CapabilityDeploymentApply + ":deploy-apply-" + string(cliOperationID("deploy-apply", "prod", "api", "ghcr.io/example/api@sha256:"+strings.Repeat("b", 64))[len("deploy-apply-"):]),
			"call:v1." + api.CapabilityDeploymentObserve,
		}
		if !reflect.DeepEqual(stub.calls, wantCalls) {
			t.Fatalf("workload=%s calls=%v want=%v", test.workload, stub.calls, wantCalls)
		}
		var observeParams api.DeploymentObserveParams
		if err := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentObserve], &observeParams); err != nil {
			t.Fatal(err)
		}
		if observeParams.Kind != test.wantKind || observeParams.ResourceType != test.wantResourceType {
			t.Fatalf("workload=%s observeParams=%+v", test.workload, observeParams)
		}
		var applyParams api.DeploymentApplyParams
		if err := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentApply], &applyParams); err != nil {
			t.Fatal(err)
		}
		if string(applyParams.Manifest) != string(manifest) {
			t.Fatalf("workload=%s manifest=%s", test.workload, applyParams.Manifest)
		}
	}
}

func TestDeployToClusterSurfacesMissingCapabilitiesAndErrors(t *testing.T) {
	empty := &pluginHost{}
	if _, err := deployToCluster(context.Background(), empty, nil, "service", "api", "prod", "image@sha256:x", "2m"); err == nil ||
		!strings.Contains(err.Error(), api.CapabilityDeploymentApply) {
		t.Fatalf("err=%v", err)
	}

	applyOnly := &pluginHost{clients: []pluginClient{&stubDeploymentPlugin{capabilities: []string{api.CapabilityDeploymentApply}}}}
	if _, err := deployToCluster(context.Background(), applyOnly, nil, "service", "api", "prod", "image@sha256:x", "2m"); err == nil ||
		!strings.Contains(err.Error(), api.CapabilityDeploymentObserve) {
		t.Fatalf("err=%v", err)
	}

	failingApply := &pluginHost{clients: []pluginClient{&stubDeploymentPlugin{capabilities: allDeploymentCapabilities(), err: errors.New("apply refused")}}}
	if _, err := deployToCluster(context.Background(), failingApply, nil, "service", "api", "prod", "image@sha256:x", "2m"); err == nil ||
		!strings.Contains(err.Error(), "apply refused") {
		t.Fatalf("err=%v", err)
	}
}

// TestRollbackClusterUndoesThenObservesRolloutStatus drives
// rollbackCluster directly against a stub deployment plugin: the undo
// itself is a mutating CallWithIdempotency, and the follow-up rollout
// status wait (equivalent to the old `kubectl rollout undo` + `kubectl
// rollout status` pair) is a plain read-only Call against the
// Deployment resource type.
func TestRollbackClusterUndoesThenObservesRolloutStatus(t *testing.T) {
	stub := &stubDeploymentPlugin{
		capabilities:  allDeploymentCapabilities(),
		observeResult: api.DeploymentObserveResult{Output: "deployment \"api\" successfully rolled out", Ready: true},
	}
	host := &pluginHost{clients: []pluginClient{stub}}
	output, err := rollbackCluster(context.Background(), host, "prod", "api", 2, "2m")
	if err != nil {
		t.Fatal(err)
	}
	if output != "deployment \"api\" successfully rolled out" {
		t.Fatalf("output=%q", output)
	}
	wantOperationID := cliOperationID("rollback", "prod", "api", "2")
	wantCalls := []string{
		"idempotent:v1." + api.CapabilityDeploymentRollback + ":" + string(wantOperationID),
		"call:v1." + api.CapabilityDeploymentObserve,
	}
	if !reflect.DeepEqual(stub.calls, wantCalls) {
		t.Fatalf("calls=%v want=%v", stub.calls, wantCalls)
	}
	var rollbackParams api.DeploymentRollbackParams
	if err := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentRollback], &rollbackParams); err != nil {
		t.Fatal(err)
	}
	if rollbackParams.Namespace != "prod" || rollbackParams.Name != "api" || rollbackParams.ToRevision != 2 {
		t.Fatalf("rollbackParams=%+v", rollbackParams)
	}
	var observeParams api.DeploymentObserveParams
	if err := json.Unmarshal(stub.lastParams["v1."+api.CapabilityDeploymentObserve], &observeParams); err != nil {
		t.Fatal(err)
	}
	if observeParams.Kind != "rollout-status" || observeParams.ResourceType != "deployment" || observeParams.Name != "api" {
		t.Fatalf("observeParams=%+v", observeParams)
	}
}

func TestRollbackClusterSurfacesMissingCapabilitiesAndErrors(t *testing.T) {
	empty := &pluginHost{}
	if _, err := rollbackCluster(context.Background(), empty, "prod", "api", 0, "2m"); err == nil ||
		!strings.Contains(err.Error(), api.CapabilityDeploymentRollback) {
		t.Fatalf("err=%v", err)
	}
	failing := &pluginHost{clients: []pluginClient{&stubDeploymentPlugin{capabilities: allDeploymentCapabilities(), err: errors.New("rollback refused")}}}
	if _, err := rollbackCluster(context.Background(), failing, "prod", "api", 0, "2m"); err == nil ||
		!strings.Contains(err.Error(), "rollback refused") {
		t.Fatalf("err=%v", err)
	}
}

// TestRunDeployAndRollbackWithoutAConfiguredPluginFailCleanly proves the
// CLI wiring: a real (non-dry-run) deploy/rollback now attempts to
// dispatch through the deployment plugin rather than shelling to
// kubectl, so without --plugin-dir pointing at one, both fail closed
// with a clear "no installed plugin" message instead of silently
// invoking a container-runtime exec function that no longer does
// anything for this path. execute is asserted never called: it is
// retained on the signature only for CLI-boundary stability (see
// runDeploy/runRollback's own doc comments).
func TestRunDeployAndRollbackWithoutAConfiguredPluginFailCleanly(t *testing.T) {
	freshOperationJournal(t)
	execute := func(string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("execute must not be called; Kubernetes operations go through the deployment plugin now")
		return nil
	}
	var stdout, stderr bytes.Buffer
	image := "ghcr.io/example/api@sha256:" + strings.Repeat("b", 64)
	if code := runDeploy(context.Background(), []string{"--yes", "--name", "api", "--namespace", "prod", image}, &stdout, &stderr, execute); code != 1 ||
		!strings.Contains(stderr.String(), "no installed plugin provides "+api.CapabilityDeploymentApply) {
		t.Fatalf("deploy code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := runRollback(context.Background(), []string{"--yes", "--namespace", "prod", "--to-revision", "2", "api"}, &stdout, &stderr, execute); code != 1 ||
		!strings.Contains(stderr.String(), "no installed plugin provides "+api.CapabilityDeploymentRollback) {
		t.Fatalf("rollback code=%d stderr=%s", code, stderr.String())
	}
}

func TestLifecycleMetricsDryRunsWriteNothing(t *testing.T) {
	layoutName := buildPublishLayout(t, "example/service", "v1")
	root := t.TempDir()
	publishReports := filepath.Join(root, "publish")
	deployReports := filepath.Join(root, "deploy")
	var stdout, stderr bytes.Buffer
	if code := runPublish(context.Background(), []string{"--dry-run", "--allow-incomplete-evidence", "--reports", publishReports, layoutName, "registry.example/service:v1"}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("publish dry-run code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	image := "registry.example/service@sha256:" + strings.Repeat("c", 64)
	if code := runDeploy(context.Background(), []string{"--dry-run", "--reports", deployReports, image}, &stdout, &stderr, nil); code != 0 {
		t.Fatalf("deploy dry-run code=%d stderr=%s", code, stderr.String())
	}
	for _, path := range []string{publishReports, deployReports} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run wrote metrics directory %s: %v", path, err)
		}
	}
}

// A CLI-level "successful non-dry-run deploy" test (this package used to
// have TestDeploymentSuccessIsNotReclassifiedWhenMetricsFail here,
// proving a metrics-write failure after a successful deploy is reported
// as a warning rather than reclassifying the deploy itself as failed)
// cannot be driven the same way anymore: reaching runDeploy's success
// path now requires a real deployment plugin talking to a live cluster
// (see deployToCluster), not a stubbed containerExecutor - see
// TestDeployToClusterAppliesThenObservesPerWorkload above for the
// unit-level coverage of deployToCluster's own logic, and this file's
// accompanying report for why the CLI-boundary "success" path itself is
// untestable without a live cluster in this environment. The metrics
// code path (deployOperationCount is unconditional; the write failure is
// only ever logged as a warning, never turned into a non-zero exit code)
// is unchanged by this refactor.

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
	if code := runRollback(context.Background(), []string{"api"}, &stdout, &stderr, nil); code != 2 {
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
}

func TestLifecycleDryRunsErrorsAndDispatch(t *testing.T) {
	freshOperationJournal(t)
	var stdout, stderr bytes.Buffer
	if code := runRollback(context.Background(), []string{"--dry-run", "--to-revision", "3", "api"}, &stdout, &stderr, nil); code != 0 ||
		!strings.Contains(stdout.String(), "--to-revision=3") {
		t.Fatalf("rollback dry-run code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	// A real (non-dry-run) deploy no longer shells to kubectl through
	// execute at all (see deployToCluster): TestRunDeployAndRollbackWithoutAConfiguredPluginFailCleanly
	// covers that failure mode (code 1, "no installed plugin ...") in
	// full; this test keeps only the --yes gate itself, which is
	// unaffected by the plugin rewrite.
	image := "registry.example/api@sha256:" + strings.Repeat("c", 64)
	if code := runDeploy(context.Background(), []string{image}, &stdout, &stderr, nil); code != 2 {
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
	} {
		if code := call(); code != 2 {
			t.Fatalf("missing arguments code=%d", code)
		}
	}
	// Rollback without a raw deployment name is now the project-native form;
	// outside a project it reports the missing deployed identity as a product
	// precondition rather than a syntax error.
	if code := runRollback(context.Background(), nil, &stdout, &stderr, nil); code != 1 {
		t.Fatalf("project rollback without state code=%d", code)
	}
}

func TestDefaultUploadSessionDirHonorsExplicitConfiguration(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_UPLOAD_SESSION_DIR", "  /tmp/platform-factory-uploads  ")
	if got := defaultUploadSessionDir(); got != "/tmp/platform-factory-uploads" {
		t.Fatalf("session directory=%q", got)
	}
}

func TestDefaultWorkloadStateRootHonorsExplicitConfiguration(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_WORKLOAD_STATE_DIR", "  /tmp/platform-factory-workload-state  ")
	if got := defaultWorkloadStateRoot(); got != "/tmp/platform-factory-workload-state" {
		t.Fatalf("workload state root=%q", got)
	}
}

func TestDefaultLifecycleRootFallsBackWithoutConfiguration(t *testing.T) {
	t.Setenv("PLATFORM_FACTORY_OPERATION_JOURNAL_DIR", "")
	got := defaultOperationJournalRoot()
	if !strings.HasSuffix(got, filepath.Join("platform-factory", "operation-journal")) {
		t.Fatalf("operation journal root=%q", got)
	}
}

// TestClaimOperationCoversEveryOutcome exercises every branch of
// claimOperation directly against a real, file-backed journal: the first
// caller for an ID proceeds; a second caller for the same ID observes
// whatever terminal (or non-terminal) status the first caller left behind.
func TestClaimOperationCoversEveryOutcome(t *testing.T) {
	journal, err := idempotency.NewFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	proceed, done, err := claimOperation(journal, "op-first", "scope")
	if !proceed || done || err != nil {
		t.Fatalf("first claim: proceed=%v done=%v err=%v", proceed, done, err)
	}

	proceed, done, err = claimOperation(journal, "op-first", "scope")
	if proceed || !done || !errors.Is(err, core.ErrOperationIndeterminate) {
		t.Fatalf("re-claim of a started, unterminated operation: proceed=%v done=%v err=%v", proceed, done, err)
	}

	if err := journal.Complete("op-first"); err != nil {
		t.Fatal(err)
	}
	proceed, done, err = claimOperation(journal, "op-first", "scope")
	if proceed || !done || err != nil {
		t.Fatalf("re-claim of a completed operation: proceed=%v done=%v err=%v", proceed, done, err)
	}

	if _, _, err := claimOperation(journal, "op-second", "scope"); err != nil {
		t.Fatal(err)
	}
	if err := journal.Fail("op-second"); err != nil {
		t.Fatal(err)
	}
	proceed, done, err = claimOperation(journal, "op-second", "scope")
	if proceed || !done || err == nil || !strings.Contains(err.Error(), "previously failed") {
		t.Fatalf("re-claim of a failed operation: proceed=%v done=%v err=%v", proceed, done, err)
	}
}

func TestClaimOperationSurfacesJournalStartError(t *testing.T) {
	sentinel := errors.New("journal unavailable")
	if _, _, err := claimOperation(failingJournal{startErr: sentinel}, "op", "scope"); !errors.Is(err, sentinel) {
		t.Fatalf("err=%v", err)
	}
}

// failingJournal is a minimal core.OperationJournal whose Start always
// fails, for exercising claimOperation's own error-wrapping branch without
// a real backing store.
type failingJournal struct {
	startErr error
}

func (failingJournal) Lookup(core.OperationID) (core.OperationRecord, bool) {
	return core.OperationRecord{}, false
}
func (f failingJournal) Start(core.OperationID, string) (bool, error) { return false, f.startErr }
func (failingJournal) Complete(core.OperationID) error                { return nil }
func (failingJournal) Fail(core.OperationID) error                    { return nil }

func TestManifestBuildersProduceValidDeterministicKubernetesResources(t *testing.T) {
	deployment := deploymentManifest("api", "prod", "registry.example/api:v1", 3, 8080, "100m", "128Mi")
	var decodedDeployment map[string]any
	if err := json.Unmarshal(deployment, &decodedDeployment); err != nil {
		t.Fatalf("deploymentManifest produced invalid JSON: %v", err)
	}
	if decodedDeployment["kind"] != "Deployment" {
		t.Fatalf("kind=%v", decodedDeployment["kind"])
	}
	spec := decodedDeployment["spec"].(map[string]any)
	if spec["replicas"] != float64(3) {
		t.Fatalf("replicas=%v", spec["replicas"])
	}
	container := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if container["image"] != "registry.example/api:v1" {
		t.Fatalf("image=%v", container["image"])
	}
	securityContext := container["securityContext"].(map[string]any)
	if securityContext["allowPrivilegeEscalation"] != false || securityContext["readOnlyRootFilesystem"] != true {
		t.Fatalf("container securityContext=%v", securityContext)
	}
	if deployment2 := deploymentManifest("api", "prod", "registry.example/api:v1", 3, 8080, "100m", "128Mi"); !bytes.Equal(deployment, deployment2) {
		t.Fatal("deploymentManifest is not deterministic for identical inputs")
	}

	service := serviceManifest("api", "prod", 8080)
	var decodedService map[string]any
	if err := json.Unmarshal(service, &decodedService); err != nil {
		t.Fatalf("serviceManifest produced invalid JSON: %v", err)
	}
	if decodedService["kind"] != "Service" {
		t.Fatalf("kind=%v", decodedService["kind"])
	}
	port := decodedService["spec"].(map[string]any)["ports"].([]any)[0].(map[string]any)
	if port["port"] != float64(8080) || port["targetPort"] != float64(8080) {
		t.Fatalf("service port=%v", port)
	}

	job := jobManifest("migrate", "prod", "registry.example/migrate:v1", "200m", "256Mi")
	var decodedJob map[string]any
	if err := json.Unmarshal(job, &decodedJob); err != nil {
		t.Fatalf("jobManifest produced invalid JSON: %v", err)
	}
	if decodedJob["kind"] != "Job" {
		t.Fatalf("kind=%v", decodedJob["kind"])
	}
	jobSpec := decodedJob["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if jobSpec["restartPolicy"] != "Never" {
		t.Fatalf("job restartPolicy=%v", jobSpec["restartPolicy"])
	}

	single := combinedManifest(deployment)
	if !bytes.Equal(single, deployment) {
		t.Fatal("combinedManifest with a single document must return it unwrapped")
	}
	combined := combinedManifest(deployment, service)
	var decodedList map[string]any
	if err := json.Unmarshal(combined, &decodedList); err != nil {
		t.Fatalf("combinedManifest produced invalid JSON: %v", err)
	}
	if decodedList["kind"] != "List" {
		t.Fatalf("kind=%v", decodedList["kind"])
	}
	if items := decodedList["items"].([]any); len(items) != 2 {
		t.Fatalf("combined items=%d, want 2", len(items))
	}
}

// failingStore is a minimal workloadstate.Store whose Lookup and Save
// return configurable errors, exercising transitionPublishWorkload's
// error branches without a real filesystem-backed store.
type failingStore struct {
	lookupState core.RuntimeState
	lookupFound bool
	lookupErr   error
	saveErr     error
}

func (f failingStore) Lookup(core.WorkloadID) (core.RuntimeState, bool, error) {
	return f.lookupState, f.lookupFound, f.lookupErr
}
func (f failingStore) Save(core.WorkloadID, core.RuntimeState) error { return f.saveErr }

func TestTransitionPublishWorkload(t *testing.T) {
	if warning, ok := publish.TransitionWorkload(nil, "w", core.PhasePublishing); !ok || warning != "" {
		t.Fatalf("nil store: warning=%q ok=%t", warning, ok)
	}

	sentinel := errors.New("lookup boom")
	if warning, ok := publish.TransitionWorkload(failingStore{lookupErr: sentinel}, "w", core.PhasePublishing); ok || !strings.Contains(warning, "lookup boom") {
		t.Fatalf("lookup error: warning=%q ok=%t", warning, ok)
	}

	// Not found defaults to PhaseBuilt, from which PhasePublishing is a
	// valid transition per internal/core/statemachine.go's own table.
	store := failingStore{lookupFound: false}
	if warning, ok := publish.TransitionWorkload(store, "w", core.PhasePublishing); !ok || warning != "" {
		t.Fatalf("not found: warning=%q ok=%t", warning, ok)
	}

	// PhaseBuilt -> PhaseDeploying is not a valid direct transition.
	invalidFrom := failingStore{lookupFound: true, lookupState: core.RuntimeState{Phase: core.PhaseBuilt}}
	if warning, ok := publish.TransitionWorkload(invalidFrom, "w", core.PhaseDeploying); ok || warning == "" {
		t.Fatalf("invalid transition: warning=%q ok=%t", warning, ok)
	}

	saveSentinel := errors.New("save boom")
	saveFails := failingStore{lookupFound: true, lookupState: core.RuntimeState{Phase: core.PhaseBuilt}, saveErr: saveSentinel}
	if warning, ok := publish.TransitionWorkload(saveFails, "w", core.PhasePublishing); ok || !strings.Contains(warning, "save boom") {
		t.Fatalf("save error: warning=%q ok=%t", warning, ok)
	}

	real, err := workloadstate.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if warning, ok := publish.TransitionWorkload(real, "w", core.PhasePublishing); !ok || warning != "" {
		t.Fatalf("real store success: warning=%q ok=%t", warning, ok)
	}
}

func TestTcpProbeAndResourceRequestsShapes(t *testing.T) {
	probe := tcpProbe(8080, 5, 10)
	if probe["initialDelaySeconds"] != 5 || probe["periodSeconds"] != 10 {
		t.Fatalf("probe=%v", probe)
	}
	tcpSocket, ok := probe["tcpSocket"].(map[string]any)
	if !ok || tcpSocket["port"] != 8080 {
		t.Fatalf("probe tcpSocket=%v", probe["tcpSocket"])
	}

	resources := resourceRequests("250m", "512Mi")
	requests, ok := resources["requests"].(map[string]string)
	if !ok || requests["cpu"] != "250m" || requests["memory"] != "512Mi" {
		t.Fatalf("resources=%v", resources)
	}
}
