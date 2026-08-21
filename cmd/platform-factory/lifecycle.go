package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/app/deploy"
	"github.com/CYPT71/platform-factory/internal/app/observe"
	"github.com/CYPT71/platform-factory/internal/app/publish"
	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/core"
	"github.com/CYPT71/platform-factory/internal/idempotency"
	"github.com/CYPT71/platform-factory/internal/layout"
	"github.com/CYPT71/platform-factory/internal/observability"
	"github.com/CYPT71/platform-factory/internal/policy"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/publicationtarget"
	"github.com/CYPT71/platform-factory/internal/registry"
	"github.com/CYPT71/platform-factory/internal/shellquote"
	"github.com/CYPT71/platform-factory/internal/strictjson"
	"github.com/CYPT71/platform-factory/internal/workloadstate"
	api "github.com/CYPT71/platform-factory/sdk/plugin"
)

// operationJournalFor is replaceable by hermetic tests.
var operationJournalFor = func() (core.OperationJournal, error) {
	root := defaultOperationJournalRoot()
	journal, err := idempotency.NewFileJournal(root)
	if err != nil {
		return nil, fmt.Errorf("%w (default journal directory is %q; if it is not "+
			"writable — e.g. non-root container, no $HOME — set "+
			"$PLATFORM_FACTORY_OPERATION_JOURNAL_DIR to a writable path instead)", err, root)
	}
	return journal, nil
}

func defaultOperationJournalRoot() string {
	return defaultLifecycleRoot("PLATFORM_FACTORY_OPERATION_JOURNAL_DIR", "operation-journal")
}

// workloadStateStoreFor is replaceable by hermetic tests.
var workloadStateStoreFor = func() (workloadstate.Store, error) {
	root := defaultWorkloadStateRoot()
	store, err := workloadstate.NewFileStore(root)
	if err != nil {
		return nil, fmt.Errorf("%w (default workload state directory is %q; if it is not "+
			"writable — e.g. non-root container, no $HOME — set "+
			"$PLATFORM_FACTORY_WORKLOAD_STATE_DIR to a writable path instead)", err, root)
	}
	return store, nil
}

func defaultWorkloadStateRoot() string {
	return defaultLifecycleRoot("PLATFORM_FACTORY_WORKLOAD_STATE_DIR", "workload-state")
}

func defaultLifecycleRoot(environment, name string) string {
	if configured := strings.TrimSpace(os.Getenv(environment)); configured != "" {
		return configured
	}
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "platform-factory", name)
	}
	return filepath.Join(".platform-factory", name)
}

// cliOperationID deterministically identifies one logical mutation.
func cliOperationID(domain string, parts ...string) core.OperationID {
	return core.OperationID(domain + "-" + core.DeriveID("platform-factory/"+domain+"/v1", parts...))
}

// cliWorkloadID deterministically identifies one workload with a safe filename.
func cliWorkloadID(domain string, parts ...string) core.WorkloadID {
	return core.WorkloadID(domain + "-" + core.DeriveID("platform-factory/workload/"+domain+"/v1", parts...))
}

// claimOperation prevents replay of completed, failed, or indeterminate work.
func claimOperation(journal core.OperationJournal, id core.OperationID, scope string) (proceed, done bool, doneErr error) {
	started, err := journal.Start(id, scope)
	if err != nil {
		return false, false, fmt.Errorf("claim operation: %w", err)
	}
	if started {
		return true, false, nil
	}
	record, found := journal.Lookup(id)
	if !found {
		return false, false, fmt.Errorf("claim operation: %q was not started but is also not recorded", id)
	}
	switch record.Status {
	case core.OperationCompleted:
		return false, true, nil
	case core.OperationFailed:
		return false, true, fmt.Errorf("operation %q previously failed; investigate before retrying", id)
	default:
		return false, true, fmt.Errorf("operation %q: %w", id, core.ErrOperationIndeterminate)
	}
}

// claimRecovery creates a separately claimed attempt only after the original
// publication is proven indeterminate or failed. The caller then re-observes
// the registry through the digest-addressed publication path.
func claimRecovery(journal core.OperationJournal, original core.OperationID, scope, token string) (core.OperationID, bool, bool, error) {
	if token == "" || len(token) > 64 || !core.ValidOperationID(core.OperationID(token)) {
		return "", false, false, errors.New("recovery token must be 1..64 printable characters without slash, backslash or colon")
	}
	record, found := journal.Lookup(original)
	if !found {
		return "", false, false, fmt.Errorf("original operation %q does not exist", original)
	}
	if record.Scope != scope {
		return "", false, false, fmt.Errorf("original operation %q belongs to a different digest or target", original)
	}
	if record.Status == core.OperationCompleted {
		return "", false, true, nil
	}
	recovery := core.OperationID(string(original) + "-recovery-" + core.DeriveID("platform-factory/publish-recovery/v1", token))
	proceed, done, err := claimOperation(journal, recovery, scope)
	return recovery, proceed, done, err
}

var pushOCI = func(ctx context.Context, layoutName string, target registry.Reference, sourceReference, scheme, username, password, mountFrom, sessionDir string) (registry.Result, error) {
	client := &registry.Client{
		Scheme: scheme, Username: username, Password: password,
		MountFrom: mountFrom, SessionDir: sessionDir,
	}
	if err := client.CleanupSessions(7 * 24 * time.Hour); err != nil {
		return registry.Result{}, err
	}
	return client.PushLayoutByDigest(ctx, layoutName, target, sourceReference)
}

var tagOCI = func(ctx context.Context, layoutName string, target registry.Reference, sourceReference, scheme, username, password string) error {
	client := &registry.Client{Scheme: scheme, Username: username, Password: password}
	return client.TagLayout(ctx, layoutName, target, sourceReference)
}

var pushOCIArtifact = func(ctx context.Context, target registry.Reference, published registry.Result, artifactType, payloadType string, payload []byte, scheme, username, password string) (registry.ArtifactResult, error) {
	client := &registry.Client{Scheme: scheme, Username: username, Password: password}
	return client.PushArtifact(ctx, target, published.Digest, published.MediaType, published.Size,
		artifactType, payloadType, payload)
}

func selectedPublicationDigest(report layout.Report, reference string) (string, error) {
	if len(report.Platforms) == 0 {
		return "", errors.New("verified layout contains no platform manifest")
	}
	if reference == "" {
		if len(report.Platforms) != 1 {
			return "", errors.New("verified layout contains multiple manifests; select one with --source-ref")
		}
		return report.Platforms[0].Digest, nil
	}
	for _, platform := range report.Platforms {
		if platform.Reference == reference {
			return platform.Digest, nil
		}
	}
	return "", fmt.Errorf("source reference %q is not present in the verified layout", reference)
}

var verifyRemoteDigest = func(ctx context.Context, repository, digest, scheme, username, password string) error {
	target, err := registry.ParseReference(repository + ":verification")
	if err != nil {
		return err
	}
	client := &registry.Client{Scheme: scheme, Username: username, Password: password}
	_, _, err = client.GetManifest(ctx, target, digest)
	return err
}

func printPublishUsage(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(output, `platform-factory publish — push a verified OCI layout to a registry, with SBOM/provenance/signature artifacts and a policy gate before any tag moves

Usage:
  platform-factory publish [OPTIONS] [LAYOUT] IMAGE
  platform-factory publish --deploy-only [OPTIONS]

Inside a pf.yaml project, "platform-factory publish" with no LAYOUT
discovers the project's own release bundle (SBOM, provenance, policy
evidence) instead of requiring you to pass every artifact by hand.
--push-only is the default and explicit; --deploy-only hands off to
"platform-factory deploy" using the digest this project already published.

Examples:
  platform-factory publish oci-image registry.example.com/app:v1
  platform-factory publish --sign --sbom oci-image registry.example.com/app:v1
  platform-factory publish --dry-run oci-image registry.example.com/app:v1
  platform-factory publish --deploy-only

Options:`)
	flags.SetOutput(output)
	flags.PrintDefaults()
}

func runPublish(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	startedAt := time.Now()
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print operations without executing them")
	yes := flags.Bool("yes", false, "confirm registry publication")
	pushOnly := flags.Bool("push-only", false, "publish artifacts without deploying (the default; explicit for automation)")
	deployOnly := flags.Bool("deploy-only", false, "deploy the project's previously published digest without pushing")
	sign := flags.Bool("sign", false, "sign the published digest with the native Ed25519 engine")
	includeSBOM := flags.Bool("sbom", false, "generate and publish a native SBOM artifact")
	provenance := flags.String("provenance", "", "provenance predicate to publish as a linked artifact")
	var externalAttestations stringList
	flags.Var(&externalAttestations, "attestation", "external predicate document to subject-bind, sign, and publish; repeatable")
	journal := flags.String("journal", "", "pipeline journal used to generate native SLSA provenance")
	builderID := flags.String("builder-id", "https://platform-factory.dev/builder/v1", "SLSA builder identity")
	keyDir := flags.String("key-dir", "", "native signing key directory (default: ~/.platform-factory/keys)")
	keyName := flags.String("key-name", "release", "native signing key name")
	policyFile := flags.String("policy", "", "native publication policy JSON")
	evidenceFile := flags.String("evidence", "", "verified build evidence JSON evaluated by --policy")
	allowIncomplete := flags.Bool("allow-incomplete-evidence", false, "development escape hatch: permit publication without the complete evidence policy")
	sourceReference := flags.String("source-ref", "", "reference to select from a multi-image layout")
	username := flags.String("username", "", "registry username (default: PLATFORM_FACTORY_REGISTRY_USERNAME)")
	insecureRegistry := flags.Bool("insecure-registry", false, "use plain HTTP for an explicitly trusted development registry")
	mountFrom := flags.String("mount-from", "", "source repository for cross-repository blob mounting")
	uploadSessionDir := flags.String("upload-session-dir", defaultUploadSessionDir(), "persistent registry upload session directory")
	outputFormat := flags.String("format", "json", "result format: json or reference")
	reportsDir := flags.String("reports", "", "write versioned publication metrics to DIR/metrics.json")
	recoveryToken := flags.String("recover-operation", "", "explicit unique token to reconcile and retry a failed or indeterminate publication")
	if containsHelpFlag(args) {
		printPublishUsage(stdout, flags)
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *pushOnly && *deployOnly {
		fmt.Fprintln(stderr, "platform-factory publish: --push-only and --deploy-only are mutually exclusive")
		return 2
	}
	if *deployOnly {
		if flags.NArg() != 0 {
			fmt.Fprintln(stderr, "platform-factory publish: --deploy-only consumes the persisted project digest and takes no IMAGE")
			return 2
		}
		deployArgs := []string{}
		if *dryRun {
			deployArgs = append(deployArgs, "--dry-run")
		}
		if *yes {
			deployArgs = append(deployArgs, "--yes")
		}
		return runDeploy(ctx, deployArgs, stdout, stderr, execute)
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		fmt.Fprintln(stderr, "usage: platform-factory publish [OPTIONS] [LAYOUT] IMAGE")
		return 2
	}
	if *provenance != "" && *journal != "" {
		fmt.Fprintln(stderr, "platform-factory publish: --provenance and --journal are mutually exclusive")
		return 2
	}
	if len(externalAttestations) > 0 && !*sign {
		fmt.Fprintln(stderr, "platform-factory publish: --attestation requires --sign")
		return 2
	}
	if err := publish.ValidateExternalAttestations(externalAttestations); err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: %v\n", err)
		return 2
	}
	var discoveredProject *project.Loaded
	if flags.NArg() == 1 {
		loaded, discoverErr := project.Discover(".", "")
		if discoverErr != nil {
			fmt.Fprintf(stderr, "platform-factory publish: discover project build: %v\n", discoverErr)
			return 2
		}
		discoveredProject = &loaded
		releaseDir := filepath.Join(loaded.Root, ".platform-factory", "release")
		reportsDir := filepath.Join(releaseDir, "reports")
		if !*allowIncomplete && *provenance == "" && *journal == "" && *policyFile == "" && *evidenceFile == "" && !*includeSBOM && !*sign {
			for _, required := range []string{
				filepath.Join(releaseDir, "sbom.json"), filepath.Join(releaseDir, "provenance.json"),
				filepath.Join(reportsDir, "policy-rules.json"), filepath.Join(reportsDir, "evidence.json"),
			} {
				if info, statErr := os.Lstat(required); statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
					fmt.Fprintf(stderr, "platform-factory publish: release bundle is incomplete or unsafe at %s (run `pf build` again)\n", required)
					return 1
				}
			}
			*includeSBOM = true
			*sign = true
			*provenance = filepath.Join(releaseDir, "provenance.json")
			*policyFile = filepath.Join(reportsDir, "policy-rules.json")
			*evidenceFile = filepath.Join(reportsDir, "evidence.json")
		}
	}
	if !*allowIncomplete && (!*includeSBOM || !*sign ||
		(*provenance == "" && *journal == "") || *policyFile == "" || *evidenceFile == "") {
		fmt.Fprintln(stderr, "platform-factory publish: production publication requires --sbom, --sign, --provenance or --journal, --policy and --evidence (or explicitly use --allow-incomplete-evidence for development)")
		return 2
	}
	if *outputFormat != "json" && *outputFormat != "reference" {
		fmt.Fprintln(stderr, "platform-factory publish: format must be json or reference")
		return 2
	}
	if !*dryRun && !*yes {
		fmt.Fprintln(stderr, "platform-factory publish: publication changes a registry; pass --yes or preview with --dry-run")
		return 2
	}
	var layoutName, image string
	if flags.NArg() == 1 {
		layoutName, image = discoveredProject.Output(), strings.TrimPrefix(flags.Arg(0), "docker://")
	} else {
		layoutName, image = flags.Arg(0), strings.TrimPrefix(flags.Arg(1), "docker://")
	}
	report, err := layout.Verify(layoutName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: verify project layout %s: %v (run `pf build` first)\n", layoutName, err)
		return 1
	}
	references := map[string]bool{}
	for _, platform := range report.Platforms {
		if platform.Reference != "" {
			references[platform.Reference] = true
		}
	}
	if *sourceReference == "" && len(references) == 1 {
		for reference := range references {
			*sourceReference = reference
		}
	}
	if *sourceReference == "" && len(references) > 1 {
		fmt.Fprintln(stderr, "platform-factory publish: layout contains multiple image references; select one with --source-ref")
		return 2
	}
	target, err := registry.ParseReference(image)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: %v\n", err)
		return 2
	}
	if *username == "" {
		*username = strings.TrimSpace(os.Getenv("PLATFORM_FACTORY_REGISTRY_USERNAME"))
	}
	localDigest, err := selectedPublicationDigest(report, *sourceReference)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: %v\n", err)
		return 2
	}
	password := os.Getenv("PLATFORM_FACTORY_REGISTRY_PASSWORD")
	scheme := "https"
	if *insecureRegistry {
		scheme = "http"
	}
	if *policyFile != "" {
		decision, policyErr := publish.EvaluatePolicy(*policyFile, *evidenceFile,
			registry.Result{Digest: localDigest}, *includeSBOM, *provenance != "" || *journal != "", *sign)
		if policyErr != nil {
			fmt.Fprintf(stderr, "platform-factory publish: policy preflight: %v\n", policyErr)
			return 1
		}
		if !decision.Allowed {
			fmt.Fprintf(stderr, "platform-factory publish: policy denied publication before upload: %s\n", strings.Join(decision.Reasons, "; "))
			return 1
		}
		if *dryRun {
			fmt.Fprintln(stdout, "policy preflight allowed the digest-bound release bundle")
		}
	}
	if *dryRun {
		fmt.Fprintf(stdout, "native OCI Distribution push %s -> %s/%s:%s (manifest digest first, tag last)\n",
			layoutName, target.Registry, target.Repository, target.Tag)
		if *includeSBOM {
			fmt.Fprintln(stdout, "generate native SBOM and publish it as an OCI subject artifact")
		}
		if *sign {
			fmt.Fprintln(stdout, "create a native DSSE/Ed25519 signature and publish it as an OCI subject artifact")
		}
		if *provenance != "" {
			fmt.Fprintln(stdout, "publish provenance as an OCI subject artifact")
		}
		if *journal != "" {
			fmt.Fprintln(stdout, "generate SLSA provenance from the pipeline journal and publish it as an OCI subject artifact")
		}
		for _, attestationPath := range externalAttestations {
			fmt.Fprintf(stdout, "validate, subject-bind, sign, and publish external attestation %s as an OCI subject artifact\n", attestationPath)
		}
		fmt.Fprintf(stdout, "move tag %s only after every evidence artifact and policy check succeeds\n", target.Tag)
		return 0
	}

	opJournal, err := operationJournalFor()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: open operation journal: %v\n", err)
		return 1
	}
	publishScope := target.Registry + "/" + target.Repository + ":" + target.Tag
	operationScope := "publish:" + publishScope + "@" + localDigest
	baseOperationID := cliOperationID("publish", target.Registry, target.Repository, target.Tag, *sourceReference, localDigest)
	opID := baseOperationID
	var proceed, done bool
	var doneErr error
	if *recoveryToken == "" {
		proceed, done, doneErr = claimOperation(opJournal, opID, operationScope)
	} else {
		opID, proceed, done, doneErr = claimRecovery(opJournal, baseOperationID, operationScope, *recoveryToken)
	}
	if done {
		if doneErr != nil {
			fmt.Fprintf(stderr, "platform-factory publish: %v\n", doneErr)
			return 1
		}
		fmt.Fprintf(stderr, "platform-factory publish: %s -> %s already published (operation %s); not repeating the push\n",
			layoutName, publishScope, opID)
		return 0
	}
	if !proceed {
		fmt.Fprintf(stderr, "platform-factory publish: %v\n", doneErr)
		return 1
	}
	workloadID := cliWorkloadID("publish", target.Registry, target.Repository, target.Tag)
	stateStore, err := workloadStateStoreFor()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: open workload state store: %v\n", err)
		return 1
	}
	if warning, ok := publish.TransitionWorkload(stateStore, workloadID, core.PhasePublishing); !ok {
		fmt.Fprintf(stderr, "platform-factory publish: %s\n", warning)
	}

	succeeded := false
	defer func() {
		if succeeded {
			_ = opJournal.Complete(opID)
			if warning, ok := publish.TransitionWorkload(stateStore, workloadID, core.PhasePublished); !ok {
				fmt.Fprintf(stderr, "platform-factory publish: %s\n", warning)
			}
		} else {
			_ = opJournal.Fail(opID)
			if warning, ok := publish.TransitionWorkload(stateStore, workloadID, core.PhaseFailed); !ok {
				fmt.Fprintf(stderr, "platform-factory publish: %s\n", warning)
			}
		}
	}()

	published, err := pushOCI(ctx, layoutName, target, *sourceReference, scheme, *username, password, *mountFrom, *uploadSessionDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: native registry push: %v\n", err)
		return 1
	}
	digest := published.Digest
	immutable := published.Reference
	if digest != localDigest {
		fmt.Fprintf(stderr, "platform-factory publish: registry digest %s does not match verified build digest %s\n", digest, localDigest)
		return 1
	}
	if !validDigestReference(immutable) {
		fmt.Fprintf(stderr, "platform-factory publish: registry returned invalid digest %q\n", digest)
		return 1
	}
	_ = execute // retained in the command boundary for deploy/rollback test injection.
	artifacts, err := publish.BuildArtifactsWithAttestations(layoutName, published, *includeSBOM,
		*provenance, *journal, *builderID, *sign, *keyDir, *keyName, externalAttestations)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: build native evidence: %v\n", err)
		return 1
	}
	for _, artifact := range artifacts {
		if _, err := pushOCIArtifact(ctx, target, published,
			artifact.ArtifactType, artifact.PayloadType, artifact.Payload, scheme, *username, password); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: publish %s: %v\n", artifact.Name, err)
			return 1
		}
	}
	if *policyFile != "" {
		decision, err := publish.EvaluatePolicy(*policyFile, *evidenceFile, published,
			*includeSBOM, *provenance != "" || *journal != "", *sign)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: policy: %v\n", err)
			return 1
		}
		if !decision.Allowed {
			fmt.Fprintf(stderr, "platform-factory publish: policy denied tag update: %s\n", strings.Join(decision.Reasons, "; "))
			return 1
		}
	}
	if err := tagOCI(ctx, layoutName, target, *sourceReference, scheme, *username, password); err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: move registry tag after evidence: %v\n", err)
		return 1
	}
	if discoveredProject != nil {
		publicationDir := filepath.Join(discoveredProject.Root, ".platform-factory", "publication")
		if *reportsDir == "" {
			*reportsDir = publicationDir
		}
		publicationRules := policy.Rules{
			APIVersion: policy.APIVersion, RequireHardening: true, RequireSBOM: true,
			RequireProvenance: true, RequireSignature: true, RequireReproducible: true,
		}
		publicationEvidence := policy.Evidence{
			SubjectDigest: digest, NonRoot: true, ReadOnlyRootFS: true,
			CapabilitiesDropped: true, SecretsAbsent: true, SBOM: true,
			Provenance: true, Signature: true, Reproducible: true,
		}
		if err := atomicfile.WriteJSONSensitive(filepath.Join(publicationDir, "policy.json"), publicationRules); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: persist publication policy: %v\n", err)
			return 1
		}
		if err := atomicfile.WriteJSONSensitive(filepath.Join(publicationDir, "evidence.json"), publicationEvidence); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: persist publication evidence: %v\n", err)
			return 1
		}
		if err := atomicfile.WriteJSONSensitive(filepath.Join(discoveredProject.Root, ".platform-factory", "published.json"), map[string]any{
			"api_version": "platform-factory.dev/publication/v1", "digest": digest,
			"reference": immutable, "repository": target.Registry + "/" + target.Repository, "scheme": scheme,
		}); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: persist immutable published reference: %v\n", err)
			return 1
		}
	}
	// The registry tag and all required project evidence are durable at this
	// point. Telemetry failure must not relabel that real external success as a
	// failed/indeterminate publication in the write-once operation journal.
	succeeded = true
	if *reportsDir != "" {
		if err := atomicfile.WriteJSON(*reportsDir, "metrics.json", map[string]any{
			"api_version": "platform-factory.dev/metrics/v1", "operation": "publish",
			"trace_id": observability.TraceIDFromContext(ctx), "duration_ms": time.Since(startedAt).Milliseconds(),
			"digest": digest, "reference": immutable, "artifacts": len(artifacts), "tag_moved": true, "success": true,
		}); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: warning: publication succeeded but metrics could not be written: %v\n", err)
		}
	}
	if *outputFormat == "reference" {
		fmt.Fprintln(stdout, immutable)
	} else {
		result, _ := json.MarshalIndent(map[string]any{
			"api_version": cliOutputAPIVersion,
			"digest":      digest, "image": image, "reference": immutable, "valid": true,
		}, "", "  ")
		fmt.Fprintln(stdout, string(result))
	}
	return 0
}

func defaultUploadSessionDir() string {
	return defaultLifecycleRoot("PLATFORM_FACTORY_UPLOAD_SESSION_DIR", "registry-uploads")
}

func printDeployUsage(output io.Writer, flags *flag.FlagSet) {
	fmt.Fprintln(output, `platform-factory deploy — apply a digest-pinned image to Kubernetes, gated by the same policy engine as publish

Usage:
  platform-factory deploy [OPTIONS] IMAGE@sha256:DIGEST
  platform-factory deploy [OPTIONS]

With no IMAGE, deploy consumes the digest this project's own
"platform-factory publish" already recorded, so there is no chance of
deploying a different digest than the one that was actually published.
IMAGE must always be pinned by sha256 digest - a mutable tag is refused.

Examples:
  platform-factory deploy registry.example.com/app@sha256:...
  platform-factory deploy --dry-run registry.example.com/app@sha256:...
  platform-factory deploy --namespace staging --replicas 3

Options:`)
	flags.SetOutput(output)
	flags.PrintDefaults()
}

func runDeploy(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	startedAt := time.Now()
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "platform-factory", "Kubernetes Deployment name")
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	replicas := flags.Int("replicas", 1, "positive replica count")
	port := flags.Int("port", 8080, "container port")
	workload := flags.String("workload", "auto", "workload type: auto, service, job, statefulset, daemonset, or cronjob")
	schedule := flags.String("schedule", "", "five-field schedule required by --workload cronjob")
	cpuRequest := flags.String("cpu-request", "100m", "requested CPU (Kubernetes quantity)")
	memoryRequest := flags.String("memory-request", "128Mi", "requested memory (Kubernetes quantity)")
	runtimeClass := flags.String("runtime-class", "", "optional Kubernetes RuntimeClass and matching compatible-node scheduling contract")
	ingressHost := flags.String("ingress-host", "", "optional DNS host for an Ingress")
	ingressPath := flags.String("ingress-path", "/", "Ingress path prefix")
	var configValues, secretEnvValues, volumeValues repeatedFlag
	flags.Var(&configValues, "config", "non-secret ConfigMap entry KEY=VALUE (repeatable)")
	flags.Var(&secretEnvValues, "secret-env", "Secret reference ENV=SECRET/KEY; values are never read (repeatable)")
	flags.Var(&volumeValues, "volume", "persistent volume MOUNT_PATH=SIZE (repeatable)")
	timeout := flags.String("timeout", "2m", "rollout timeout")
	reportsDir := flags.String("reports", "", "write versioned deployment metrics to DIR/metrics.json")
	policyFile := flags.String("policy", "", "policy JSON to evaluate before deploying (see pf publish --policy)")
	evidenceFile := flags.String("evidence", "", "evidence JSON evaluated by --policy - e.g. one pf publish already wrote for this digest")
	dryRun := flags.Bool("dry-run", false, "print the manifest without applying it")
	yes := flags.Bool("yes", false, "confirm cluster deployment")
	outputFormat := flags.String("format", "text", "result format: text or json")
	pluginFlags := registerPluginFlags(flags)
	if containsHelpFlag(args) {
		printDeployUsage(stdout, flags)
		return 0
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 || !validOutputFormat(*outputFormat) || !validKubernetesName(*name) || !validKubernetesName(*namespace) ||
		*replicas < 1 || *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "usage: platform-factory deploy [--workload auto|service|job] [--name NAME] [--namespace NS] [--policy FILE --evidence FILE] [--dry-run] [--yes] [IMAGE]")
		return 2
	}
	if *workload != "auto" && *workload != "service" && *workload != "job" && *workload != "statefulset" && *workload != "daemonset" && *workload != "cronjob" {
		fmt.Fprintln(stderr, "platform-factory deploy: --workload must be auto, service, job, statefulset, daemonset, or cronjob")
		return 2
	}
	if *runtimeClass != "" && !validKubernetesName(*runtimeClass) {
		fmt.Fprintln(stderr, "platform-factory deploy: --runtime-class must be a valid Kubernetes name")
		return 2
	}
	configEntries, secretReferences, persistentVolumes, extensionErr := deploy.ParseKubernetesExtensions(configValues, secretEnvValues, volumeValues)
	if extensionErr != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: %v\n", extensionErr)
		return 2
	}
	if !validResourceQuantity(*cpuRequest) || !validResourceQuantity(*memoryRequest) {
		fmt.Fprintln(stderr, "platform-factory deploy: --cpu-request and --memory-request must be non-empty Kubernetes quantities")
		return 2
	}
	if !*dryRun && !*yes {
		fmt.Fprintln(stderr, "platform-factory deploy: deployment changes the live cluster; pass --yes or preview with --dry-run")
		return 2
	}
	image := ""
	var deploymentProject *project.Loaded
	if flags.NArg() == 1 {
		image = flags.Arg(0)
		if loaded, discoverErr := project.Discover(".", ""); discoverErr == nil {
			deploymentProject = &loaded
		}
	} else {
		loaded, discoverErr := project.Discover(".", "")
		if discoverErr != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: discover project publication: %v\n", discoverErr)
			return 2
		}
		deploymentProject = &loaded
		var published struct {
			APIVersion string `json:"api_version"`
			Digest     string `json:"digest"`
			Reference  string `json:"reference"`
			Repository string `json:"repository"`
			Scheme     string `json:"scheme"`
		}
		publishedPath := filepath.Join(loaded.Root, ".platform-factory", "published.json")
		if err := strictjson.DecodeFile(publishedPath, &published); err != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: no verified published release (run `pf publish` first): %v\n", err)
			return 1
		}
		if published.APIVersion != "platform-factory.dev/publication/v1" ||
			(published.Scheme != "https" && published.Scheme != "http") ||
			published.Reference != published.Repository+"@"+published.Digest {
			fmt.Fprintln(stderr, "platform-factory deploy: persisted publication is inconsistent; run `pf publish` again")
			return 1
		}
		image = published.Reference
		username := strings.TrimSpace(os.Getenv("PLATFORM_FACTORY_REGISTRY_USERNAME"))
		password := os.Getenv("PLATFORM_FACTORY_REGISTRY_PASSWORD")
		if err := verifyRemoteDigest(ctx, published.Repository, published.Digest, published.Scheme, username, password); err != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: published digest is not verifiable in the registry: %v\n", err)
			return 1
		}
		if *policyFile == "" && *evidenceFile == "" {
			publicationDir := filepath.Join(loaded.Root, ".platform-factory", "publication")
			*policyFile = filepath.Join(publicationDir, "policy.json")
			*evidenceFile = filepath.Join(publicationDir, "evidence.json")
		}
	}
	if !validDigestReference(image) {
		fmt.Fprintln(stderr, "platform-factory deploy: IMAGE must be pinned by sha256 digest")
		return 2
	}
	if *policyFile != "" {
		_, digest, _ := strings.Cut(image, "@")
		decision, err := deploy.EvaluatePolicy(*policyFile, *evidenceFile, digest)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: policy: %v\n", err)
			return 1
		}
		if !decision.Allowed {
			fmt.Fprintf(stderr, "platform-factory deploy: policy denied deployment: %s\n", strings.Join(decision.Reasons, "; "))
			return 1
		}
	}
	traceID := observability.TraceIDFromContext(ctx)
	if traceID != "" {
		observability.Info("deploy starting", observability.Fields{
			"trace_id":  traceID,
			"name":      *name,
			"namespace": *namespace,
			"image":     image,
			"replicas":  *replicas,
			"port":      *port,
		})
	}

	selectedWorkload := *workload
	if selectedWorkload == "auto" {
		selectedWorkload = "service"
		if loaded, discoverErr := project.Discover(".", ""); discoverErr == nil && len(loaded.Config.Ports) == 0 {
			selectedWorkload = "job"
		}
	}
	manifest, manifestErr := publicationtarget.KubernetesManifest(publicationtarget.KubernetesSpec{
		Workload: selectedWorkload, Name: *name, Namespace: *namespace, Image: image,
		Replicas: *replicas, Port: *port, CPURequest: *cpuRequest, MemoryRequest: *memoryRequest,
		RuntimeClass: *runtimeClass,
		Schedule:     *schedule,
		IngressHost:  *ingressHost, IngressPath: map[bool]string{true: *ingressPath}[*ingressHost != ""],
		Config: configEntries, SecretEnv: secretReferences, Volumes: persistentVolumes,
	})
	if manifestErr != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: generate Kubernetes manifest: %v\n", manifestErr)
		return 2
	}
	if *dryRun {
		fmt.Fprintf(stderr, "platform-factory deploy: selected Kubernetes %s (%s)\n", selectedWorkload, map[string]string{"job": "the project declares no listening ports", "service": "a service workload was requested or ports are declared"}[selectedWorkload])
		if *outputFormat == "json" {
			return writeDeployJSON(stdout, stderr, map[string]any{
				"api_version": cliOutputAPIVersion, "operation": "deploy", "status": "dry_run",
				"image": image, "name": *name, "namespace": *namespace, "workload": selectedWorkload,
				"runtime_class": *runtimeClass, "manifest": string(manifest),
			})
		}
		_, _ = stdout.Write(manifest)
		return 0
	}
	// Kubernetes operations now go through the deployment plugin (see
	// deployToCluster) - a real API client, not a shelled-out kubectl -
	// so execute is no longer used for them; it stays in the signature
	// only so callers/tests across the CLI command boundary don't churn.
	_ = execute
	journal, err := operationJournalFor()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: %v\n", err)
		return 1
	}
	host, err := pluginFlags.startWithJournal(ctx, journal)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: %v\n", err)
		return 1
	}
	defer host.Close()
	observeOutput, deployErr := deployToCluster(ctx, host, manifest, selectedWorkload, *name, *namespace, image, *timeout)
	code := 0
	if deployErr != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: %v\n", deployErr)
		code = 1
	} else if observeOutput != "" && *outputFormat == "text" {
		fmt.Fprintln(stdout, observeOutput)
	}
	const deployOperationCount = 2 // apply, then one workload-appropriate observation
	deploymentReceipt := ""
	metricsReceipt := ""
	if code == 0 && deploymentProject != nil {
		if *reportsDir == "" {
			*reportsDir = filepath.Join(deploymentProject.Root, ".platform-factory", "deployment")
		}
		deploymentReceipt = filepath.Join(deploymentProject.Root, ".platform-factory", "deployed.json")
		if err := atomicfile.WriteJSONSensitive(deploymentReceipt, map[string]any{
			"api_version": "platform-factory.dev/deployment/v1", "image": image,
			"name": *name, "namespace": *namespace, "workload": selectedWorkload,
			"runtime_class": *runtimeClass,
		}); err != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: persist deployment identity: %v\n", err)
			return 1
		}
	}
	if code == 0 && *reportsDir != "" {
		_, digest, _ := strings.Cut(image, "@")
		metricsReceipt = filepath.Join(*reportsDir, "metrics.json")
		if err := atomicfile.WriteJSON(*reportsDir, "metrics.json", map[string]any{
			"api_version": "platform-factory.dev/metrics/v1", "operation": "deploy",
			"trace_id": traceID, "duration_ms": time.Since(startedAt).Milliseconds(),
			"digest": digest, "namespace": *namespace, "name": *name,
			"workload": selectedWorkload, "operations": deployOperationCount, "success": true,
		}); err != nil {
			fmt.Fprintf(stderr, "platform-factory deploy: warning: deployment succeeded but metrics could not be written: %v\n", err)
		}
	}
	if code == 0 && *outputFormat == "json" {
		return writeDeployJSON(stdout, stderr, map[string]any{
			"api_version": cliOutputAPIVersion, "operation": "deploy", "status": "deployed",
			"image": image, "name": *name, "namespace": *namespace, "workload": selectedWorkload,
			"runtime_class": *runtimeClass, "observation": observeOutput, "operations": deployOperationCount,
			"deployment_receipt": deploymentReceipt, "metrics_receipt": metricsReceipt,
		})
	}
	return code
}

func writeDeployJSON(stdout, stderr io.Writer, result map[string]any) int {
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "platform-factory deploy: encode output: %v\n", err)
		return 1
	}
	return 0
}

// deployToCluster applies manifest to the cluster through the deployment
// plugin's apply capability - mutating, driven through
// Client.CallWithIdempotency with a nil result so a crash-and-retry
// (identical operation ID and params) observes a bare success instead of
// erroring on "completed without a replayable result" (see that
// method's own doc comment: mutating response bodies are never
// persisted, since they may carry secrets) - then observes
// selectedWorkload's readiness through the plugin's observe capability,
// which is read-only and dispatched through the plain Call: job
// workloads wait for job completion, cronjob workloads take one
// point-in-time summary, and every other selectedWorkload waits for its
// own rollout to finish. Returns the observation's own human-readable
// output for the caller to print - equivalent to what the kubectl
// wait/get/rollout-status subcommand this replaces would have printed.
func deployToCluster(ctx context.Context, host *pluginHost, manifest []byte, selectedWorkload, name, namespace, image, timeout string) (string, error) {
	applyClient, found := host.findCapability(api.CapabilityDeploymentApply)
	if !found {
		return "", fmt.Errorf("no installed plugin provides %s; pass --plugin-dir pointing at a directory containing the kubernetes plugin (see docs/containerd-kubernetes.md)", api.CapabilityDeploymentApply)
	}
	applyOperationID := cliOperationID("deploy-apply", namespace, name, image)
	if err := applyClient.CallWithIdempotency(ctx, applyOperationID, "v1."+api.CapabilityDeploymentApply,
		api.DeploymentApplyParams{Manifest: manifest}, nil); err != nil {
		return "", fmt.Errorf("apply: %w", err)
	}

	observeClient, found := host.findCapability(api.CapabilityDeploymentObserve)
	if !found {
		return "", fmt.Errorf("no installed plugin provides %s", api.CapabilityDeploymentObserve)
	}
	var params api.DeploymentObserveParams
	switch selectedWorkload {
	case "job":
		params = api.DeploymentObserveParams{Kind: "wait-job", Namespace: namespace, Name: name, Timeout: timeout}
	case "cronjob":
		params = api.DeploymentObserveParams{Kind: "get-cronjob", Namespace: namespace, Name: name}
	default:
		resource := map[string]string{"service": "deployment", "statefulset": "statefulset", "daemonset": "daemonset"}[selectedWorkload]
		params = api.DeploymentObserveParams{Kind: "rollout-status", Namespace: namespace, Name: name, ResourceType: resource, Timeout: timeout}
	}
	var result api.DeploymentObserveResult
	if err := observeClient.Call(ctx, "v1."+api.CapabilityDeploymentObserve, params, &result); err != nil {
		return "", fmt.Errorf("observe: %w", err)
	}
	return result.Output, nil
}

func runRollback(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	revision := flags.Int("to-revision", 0, "target revision; zero selects the previous revision")
	timeout := flags.String("timeout", "2m", "rollout timeout")
	dryRun := flags.Bool("dry-run", false, "print operations without executing them")
	yes := flags.Bool("yes", false, "confirm rollback")
	pluginFlags := registerPluginFlags(flags)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() > 1 || !validKubernetesName(*namespace) || *revision < 0 {
		fmt.Fprintln(stderr, "usage: platform-factory rollback [OPTIONS] [DEPLOYMENT]")
		return 2
	}
	deploymentName := ""
	if flags.NArg() == 1 {
		deploymentName = flags.Arg(0)
	} else {
		state, err := observe.LoadDeployedProject()
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory rollback: no deployed project (run `pf deploy` first): %v\n", err)
			return 1
		}
		if state.Workload != "service" {
			fmt.Fprintln(stderr, "platform-factory rollback: Kubernetes Jobs have no rollout history; rebuild and publish a new immutable digest")
			return 1
		}
		deploymentName = state.Name
		*namespace = state.Namespace
	}
	if !validKubernetesName(deploymentName) {
		fmt.Fprintln(stderr, "platform-factory rollback: deployment name must be a valid Kubernetes name")
		return 2
	}
	if !*dryRun && !*yes {
		fmt.Fprintln(stderr, "platform-factory rollback: rollback changes the live deployment; pass --yes or preview with --dry-run")
		return 2
	}
	target := "deployment/" + deploymentName
	if *dryRun {
		undo := []string{"rollout", "undo", target, "--namespace", *namespace}
		if *revision > 0 {
			undo = append(undo, "--to-revision="+strconv.Itoa(*revision))
		}
		// A cosmetic kubectl-equivalent-command preview only: the real
		// rollback below never shells out to kubectl at all (see
		// rollbackCluster), but rendering the same command shape here
		// keeps --dry-run's output format stable and still legible as
		// "this is the operation that would run."
		fmt.Fprintf(stdout, "%s\n", shellquote.Command("kubectl", undo))
		fmt.Fprintf(stdout, "%s\n", shellquote.Command("kubectl", []string{"rollout", "status", target, "--namespace", *namespace, "--timeout", *timeout}))
		return 0
	}
	_ = execute // Kubernetes operations now go through the deployment plugin (see rollbackCluster), not a shelled-out kubectl.
	journal, err := operationJournalFor()
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory rollback: %v\n", err)
		return 1
	}
	host, err := pluginFlags.startWithJournal(ctx, journal)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory rollback: %v\n", err)
		return 1
	}
	defer host.Close()
	output, err := rollbackCluster(ctx, host, *namespace, deploymentName, *revision, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory rollback: %v\n", err)
		return 1
	}
	if output != "" {
		fmt.Fprintln(stdout, output)
	}
	return 0
}

// rollbackCluster rolls Deployment namespace/name back through the
// deployment plugin's rollback capability - mutating, driven through
// Client.CallWithIdempotency with a nil result for the same
// crash-and-retry-safety reason deployToCluster's apply call is - then
// observes the resulting rollout's status through the plugin's observe
// capability so the caller gets the same "did it actually finish rolling
// out" signal `kubectl rollout undo` followed by `kubectl rollout
// status` used to give.
func rollbackCluster(ctx context.Context, host *pluginHost, namespace, name string, toRevision int, timeout string) (string, error) {
	rollbackClient, found := host.findCapability(api.CapabilityDeploymentRollback)
	if !found {
		return "", fmt.Errorf("no installed plugin provides %s; pass --plugin-dir pointing at a directory containing the kubernetes plugin (see docs/containerd-kubernetes.md)", api.CapabilityDeploymentRollback)
	}
	operationID := cliOperationID("rollback", namespace, name, strconv.Itoa(toRevision))
	if err := rollbackClient.CallWithIdempotency(ctx, operationID, "v1."+api.CapabilityDeploymentRollback,
		api.DeploymentRollbackParams{Namespace: namespace, Name: name, ToRevision: toRevision}, nil); err != nil {
		return "", fmt.Errorf("rollback: %w", err)
	}

	observeClient, found := host.findCapability(api.CapabilityDeploymentObserve)
	if !found {
		return "", fmt.Errorf("no installed plugin provides %s", api.CapabilityDeploymentObserve)
	}
	var result api.DeploymentObserveResult
	if err := observeClient.Call(ctx, "v1."+api.CapabilityDeploymentObserve, api.DeploymentObserveParams{
		Kind: "rollout-status", Namespace: namespace, Name: name, ResourceType: "deployment", Timeout: timeout,
	}, &result); err != nil {
		return "", fmt.Errorf("observe rollout status: %w", err)
	}
	return result.Output, nil
}

func validDigestReference(value string) bool {
	return publicationtarget.ValidDigestReference(value)
}

func validKubernetesName(value string) bool {
	return publicationtarget.ValidKubernetesName(value)
}

func deploymentManifest(name, namespace, image string, replicas, port int, cpuRequest, memoryRequest string) []byte {
	document := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": name}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": name}},
				"spec": map[string]any{
					"securityContext": map[string]any{"runAsNonRoot": true, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"containers": []any{map[string]any{
						"name": name, "image": image,
						"ports": []any{map[string]any{"containerPort": port}},
						"securityContext": map[string]any{
							"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
							"capabilities": map[string]any{"drop": []string{"ALL"}},
						},
						"readinessProbe": tcpProbe(port, 1, 5),
						"livenessProbe":  tcpProbe(port, 5, 10),
						"resources":      resourceRequests(cpuRequest, memoryRequest),
					}},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func resourceRequests(cpu, memory string) map[string]any {
	return map[string]any{"requests": map[string]string{"cpu": cpu, "memory": memory}}
}

func validResourceQuantity(value string) bool {
	return publicationtarget.ValidResourceQuantity(value)
}

func tcpProbe(port, initialDelaySeconds, periodSeconds int) map[string]any {
	return map[string]any{
		"tcpSocket":           map[string]any{"port": port},
		"initialDelaySeconds": initialDelaySeconds,
		"periodSeconds":       periodSeconds,
	}
}

func serviceManifest(name, namespace string, port int) []byte {
	document := map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"selector": map[string]string{"app.kubernetes.io/name": name},
			"ports":    []any{map[string]any{"port": port, "targetPort": port}},
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

// combinedManifest wraps multiple resources in a Kubernetes List.
func combinedManifest(documents ...[]byte) []byte {
	if len(documents) == 1 {
		return documents[0]
	}
	items := make([]json.RawMessage, len(documents))
	for i, document := range documents {
		items[i] = json.RawMessage(document)
	}
	list := map[string]any{"apiVersion": "v1", "kind": "List", "items": items}
	data, _ := json.MarshalIndent(list, "", "  ")
	return append(data, '\n')
}

func jobManifest(name, namespace, image, cpuRequest, memoryRequest string) []byte {
	document := map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"backoffLimit": 3,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app.kubernetes.io/name": name}},
				"spec": map[string]any{
					"restartPolicy":   "Never",
					"securityContext": map[string]any{"runAsNonRoot": true, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
					"containers": []any{map[string]any{
						"name": name, "image": image,
						"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
						"resources":       resourceRequests(cpuRequest, memoryRequest),
					}},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(document, "", "  ")
	return append(data, '\n')
}

func runCompletion(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: platform-factory completion <bash|zsh|fish|powershell>")
		return 2
	}
	script, ok := completionScripts[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "platform-factory completion: unsupported shell %q\n", args[0])
		return 2
	}
	fmt.Fprint(stdout, script)
	return 0
}

var completionScripts = map[string]string{
	"bash": `_platform_factory() {
  local commands="build compose diff sbom evidence pipeline status explain logs events inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version"
  COMPREPLY=($(compgen -W "$commands" -- "${COMP_WORDS[COMP_CWORD]}"))
}
complete -F _platform_factory platform-factory
`,
	"zsh": `#compdef platform-factory
_arguments '1:command:(build compose diff sbom evidence pipeline status explain logs events inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version)'
`,
	"fish": `complete -c platform-factory -f -n '__fish_use_subcommand' -a 'build compose diff sbom evidence pipeline status explain logs events inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version'
`,
	"powershell": `Register-ArgumentCompleter -Native -CommandName platform-factory -ScriptBlock {
  param($wordToComplete) 'build','compose','diff','sbom','evidence','pipeline','status','explain','logs','events','inspect','verify','publish','detect','run','deploy','rollback','launch','project','plan','freeze','microvm','completion','version' |
    Where-Object { $_ -like "$wordToComplete*" }
}
`,
}
