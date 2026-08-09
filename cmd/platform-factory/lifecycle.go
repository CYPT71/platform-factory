package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/app/sbom"
	"github.com/CYPT71/secure-oci-base/internal/attestation"
	"github.com/CYPT71/secure-oci-base/internal/layout"
	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/policy"
	provenancegen "github.com/CYPT71/secure-oci-base/internal/provenance"
	"github.com/CYPT71/secure-oci-base/internal/registry"
	"github.com/CYPT71/secure-oci-base/internal/signing"
)

var pushOCI = func(ctx context.Context, layoutName string, target registry.Reference, sourceReference, scheme, username, password, mountFrom, sessionDir string) (registry.Result, error) {
	client := &registry.Client{
		Scheme: scheme, Username: username, Password: password,
		MountFrom: mountFrom, SessionDir: sessionDir,
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

// runPublishWithContext wraps runPublish with context support for trace_id propagation.
// Sanetizer-todo item 18: End-to-end trace correlation - COMPLETE
func runPublishWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	// Extract trace_id from context for propagation
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = observability.NewTraceID("cli", "publish").String()
	}

	// Create context with trace_id
	ctx = observability.ContextWithTraceID(ctx, traceID)

	// Pass context to runPublish
	return runPublish(ctx, args, stdout, stderr, execute)
}

func runPublish(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print operations without executing them")
	yes := flags.Bool("yes", false, "confirm registry publication")
	sign := flags.Bool("sign", false, "sign the published digest with the native Ed25519 engine")
	includeSBOM := flags.Bool("sbom", false, "generate and publish a native SBOM artifact")
	provenance := flags.String("provenance", "", "provenance predicate to publish as a linked artifact")
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
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: platform-factory publish [OPTIONS] LAYOUT IMAGE")
		return 2
	}
	if *provenance != "" && *journal != "" {
		fmt.Fprintln(stderr, "platform-factory publish: --provenance and --journal are mutually exclusive")
		return 2
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
	layoutName, image := flags.Arg(0), strings.TrimPrefix(flags.Arg(1), "docker://")
	report, err := layout.Verify(layoutName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: verify layout: %v\n", err)
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
	password := os.Getenv("PLATFORM_FACTORY_REGISTRY_PASSWORD")
	scheme := "https"
	if *insecureRegistry {
		scheme = "http"
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
		return 0
	}
	published, err := pushOCI(ctx, layoutName, target, *sourceReference, scheme, *username, password, *mountFrom, *uploadSessionDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: native registry push: %v\n", err)
		return 1
	}
	digest := published.Digest
	immutable := published.Reference
	if !validDigestReference(immutable) {
		fmt.Fprintf(stderr, "platform-factory publish: registry returned invalid digest %q\n", digest)
		return 1
	}
	_ = execute // retained in the command boundary for deploy/rollback test injection.
	artifacts, err := nativePublicationArtifacts(layoutName, published, *includeSBOM,
		*provenance, *journal, *builderID, *sign, *keyDir, *keyName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory publish: build native evidence: %v\n", err)
		return 1
	}
	for _, artifact := range artifacts {
		if _, err := pushOCIArtifact(ctx, target, published,
			artifact.artifactType, artifact.payloadType, artifact.payload, scheme, *username, password); err != nil {
			fmt.Fprintf(stderr, "platform-factory publish: publish %s: %v\n", artifact.name, err)
			return 1
		}
	}
	if *policyFile != "" {
		decision, err := evaluatePublicationPolicy(*policyFile, *evidenceFile, published,
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
	if *outputFormat == "reference" {
		fmt.Fprintln(stdout, immutable)
	} else {
		result, _ := json.MarshalIndent(map[string]any{
			"digest": digest, "image": image, "reference": immutable, "valid": true,
		}, "", "  ")
		fmt.Fprintln(stdout, string(result))
	}
	return 0
}

func defaultUploadSessionDir() string {
	if configured := strings.TrimSpace(os.Getenv("PLATFORM_FACTORY_UPLOAD_SESSION_DIR")); configured != "" {
		return configured
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(".platform-factory", "registry-uploads")
	}
	return filepath.Join(cacheDir, "platform-factory", "registry-uploads")
}

func evaluatePublicationPolicy(policyPath, evidencePath string, published registry.Result, hasSBOM, hasProvenance, hasSignature bool) (policy.Decision, error) {
	if evidencePath == "" {
		return policy.Decision{}, errors.New("--evidence is required with --policy")
	}
	decode := func(path string, target any) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return err
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("file must contain exactly one JSON object")
		}
		return nil
	}
	var rules policy.Rules
	if err := decode(policyPath, &rules); err != nil {
		return policy.Decision{}, fmt.Errorf("decode rules: %w", err)
	}
	var evidence policy.Evidence
	if err := decode(evidencePath, &evidence); err != nil {
		return policy.Decision{}, fmt.Errorf("decode evidence: %w", err)
	}
	evidence.SubjectDigest = published.Digest
	evidence.SBOM = hasSBOM
	evidence.Provenance = hasProvenance
	evidence.Signature = hasSignature
	return policy.Evaluate(rules, evidence)
}

type publicationArtifact struct {
	name, artifactType, payloadType string
	payload                         []byte
}

func nativePublicationArtifacts(layoutName string, published registry.Result, includeSBOM bool, provenancePath, journalPath, builderID string, sign bool, keyDir, keyName string) ([]publicationArtifact, error) {
	var store signing.KeyStore
	var keyID string
	if sign {
		if keyDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			keyDir = filepath.Join(home, ".platform-factory", "keys")
		}
		fileStore, err := signing.NewFileKeyStore(keyDir)
		if err != nil {
			return nil, err
		}
		store = fileStore
		publicKey, err := store.PublicKey(keyName)
		if err != nil {
			return nil, err
		}
		keyID = "ed25519:" + base64.RawURLEncoding.EncodeToString(publicKey)
	}
	var artifacts []publicationArtifact
	if includeSBOM {
		sbomService := sbom.New()
		paths, err := sbomService.CollectPaths([]string{layoutName})
		if err != nil {
			return nil, err
		}
		document, err := sbomService.Generate(paths)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(document)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, publicationArtifact{
			name: "SBOM", artifactType: "application/vnd.platform-factory.sbom.v1+json",
			payloadType: "application/json", payload: payload,
		})
	}
	if provenancePath != "" || journalPath != "" {
		var payload []byte
		var err error
		if journalPath != "" {
			file, openErr := os.Open(journalPath)
			if openErr != nil {
				return nil, openErr
			}
			predicate, generateErr := provenancegen.FromJournal(file, builderID)
			_ = file.Close()
			if generateErr != nil {
				return nil, generateErr
			}
			payload, err = json.Marshal(predicate)
		} else {
			payload, err = os.ReadFile(provenancePath)
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(payload) {
			return nil, errors.New("provenance predicate must be valid JSON")
		}
		if sign {
			var predicate any
			if err := json.Unmarshal(payload, &predicate); err != nil {
				return nil, err
			}
			envelope, err := attestation.Sign(store, keyName, keyID,
				"application/vnd.in-toto+json", predicate)
			if err != nil {
				return nil, err
			}
			payload, err = json.Marshal(envelope)
			if err != nil {
				return nil, err
			}
		}
		artifacts = append(artifacts, publicationArtifact{
			name: "provenance", artifactType: "application/vnd.platform-factory.provenance.v1+json",
			payloadType: "application/json", payload: payload,
		})
	}
	if sign {
		envelope, err := attestation.Sign(store, keyName, keyID,
			"application/vnd.platform-factory.subject.v1+json",
			map[string]string{"digest": published.Digest, "reference": published.Reference})
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, publicationArtifact{
			name: "signature", artifactType: "application/vnd.platform-factory.signature.v1+json",
			payloadType: attestation.EnvelopeMediaType, payload: payload,
		})
	}
	return artifacts, nil
}

// runDeployWithContext wraps runDeploy with context support for trace_id propagation.
// Sanetizer-todo item 18: End-to-end trace correlation - COMPLETE
func runDeployWithContext(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	// Extract trace_id from context for propagation
	traceID := observability.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = observability.NewTraceID("cli", "deploy").String()
	}

	// Create context with trace_id
	ctx = observability.ContextWithTraceID(ctx, traceID)

	// Pass context to runDeploy
	return runDeploy(ctx, args, stdout, stderr, execute)
}

func runDeploy(ctx context.Context, args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "platform-factory", "Kubernetes Deployment name")
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	replicas := flags.Int("replicas", 1, "positive replica count")
	port := flags.Int("port", 8080, "container port")
	timeout := flags.String("timeout", "2m", "rollout timeout")
	dryRun := flags.Bool("dry-run", false, "print the manifest without applying it")
	yes := flags.Bool("yes", false, "confirm cluster deployment")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validKubernetesName(*name) || !validKubernetesName(*namespace) ||
		*replicas < 1 || *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, "usage: platform-factory deploy [--name NAME] [--namespace NS] [--dry-run] [--yes] IMAGE")
		return 2
	}
	if !*dryRun && !*yes {
		fmt.Fprintln(stderr, "platform-factory deploy: deployment changes the live cluster; pass --yes or preview with --dry-run")
		return 2
	}
	image := flags.Arg(0)
	if !validDigestReference(image) {
		fmt.Fprintln(stderr, "platform-factory deploy: IMAGE must be pinned by sha256 digest")
		return 2
	}
	// Sanetizer-todo item 18: Log deploy operation with trace_id for end-to-end correlation
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

	manifest := deploymentManifest(*name, *namespace, image, *replicas, *port)
	if *dryRun {
		_, _ = stdout.Write(manifest)
		return 0
	}
	operations := []externalOperation{
		{name: "kubectl", args: []string{"apply", "-f", "-"}, stdin: bytes.NewReader(manifest)},
		{name: "kubectl", args: []string{
			"rollout", "status", "deployment/" + *name,
			"--namespace", *namespace, "--timeout", *timeout,
		}},
	}
	return executeOperations("deploy", operations, false, stdout, stderr, execute)
}

func runRollback(args []string, stdout, stderr io.Writer, execute containerExecutor) int {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	namespace := flags.String("namespace", "default", "Kubernetes namespace")
	revision := flags.Int("to-revision", 0, "target revision; zero selects the previous revision")
	timeout := flags.String("timeout", "2m", "rollout timeout")
	dryRun := flags.Bool("dry-run", false, "print operations without executing them")
	yes := flags.Bool("yes", false, "confirm rollback")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || !validKubernetesName(*namespace) ||
		!validKubernetesName(flags.Arg(0)) || *revision < 0 {
		fmt.Fprintln(stderr, "usage: platform-factory rollback [OPTIONS] DEPLOYMENT")
		return 2
	}
	if !*dryRun && !*yes {
		fmt.Fprintln(stderr, "platform-factory rollback: rollback changes the live deployment; pass --yes or preview with --dry-run")
		return 2
	}
	target := "deployment/" + flags.Arg(0)
	undo := []string{"rollout", "undo", target, "--namespace", *namespace}
	if *revision > 0 {
		undo = append(undo, "--to-revision="+strconv.Itoa(*revision))
	}
	operations := []externalOperation{
		{name: "kubectl", args: undo},
		{name: "kubectl", args: []string{"rollout", "status", target, "--namespace", *namespace, "--timeout", *timeout}},
	}
	return executeOperations("rollback", operations, *dryRun, stdout, stderr, execute)
}

func validDigestReference(value string) bool {
	_, digest, found := strings.Cut(value, "@sha256:")
	if !found || len(digest) != 64 {
		return false
	}
	for _, character := range digest {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validKubernetesName(value string) bool {
	if value == "" || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

type externalOperation struct {
	name  string
	args  []string
	stdin io.Reader
}

func executeOperations(operation string, operations []externalOperation, dryRun bool, stdout, stderr io.Writer, execute containerExecutor) int {
	for _, item := range operations {
		if dryRun {
			fmt.Fprintf(stdout, "%s\n", formatCommand(item.name, item.args))
			continue
		}
		if err := execute(item.name, item.args, item.stdin, stdout, stderr); err != nil {
			var exitErr interface{ ExitCode() int }
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode()
			}
			fmt.Fprintf(stderr, "platform-factory %s: %s failed: %v\n", operation, item.name, err)
			return 1
		}
	}
	return 0
}

func formatCommand(name string, args []string) string {
	values := append([]string{name}, args...)
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, " \t\r\n'\"\\$") {
			values[index] = "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
		}
	}
	return strings.Join(values, " ")
}

func deploymentManifest(name, namespace, image string, replicas, port int) []byte {
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
  local commands="build compose diff sbom evidence pipeline inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version"
  COMPREPLY=($(compgen -W "$commands" -- "${COMP_WORDS[COMP_CWORD]}"))
}
complete -F _platform_factory platform-factory
`,
	"zsh": `#compdef platform-factory
_arguments '1:command:(build compose diff sbom evidence pipeline inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version)'
`,
	"fish": `complete -c platform-factory -f -n '__fish_use_subcommand' -a 'build compose diff sbom evidence pipeline inspect verify publish detect run deploy rollback launch project plan freeze microvm completion version'
`,
	"powershell": `Register-ArgumentCompleter -Native -CommandName platform-factory -ScriptBlock {
  param($wordToComplete) 'build','compose','diff','sbom','evidence','pipeline','inspect','verify','publish','detect','run','deploy','rollback','launch','project','plan','freeze','microvm','completion','version' |
    Where-Object { $_ -like "$wordToComplete*" }
}
`,
}
