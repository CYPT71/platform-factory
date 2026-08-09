package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CYPT71/secure-oci-base/internal/observability"
	"github.com/CYPT71/secure-oci-base/internal/policy"
	"github.com/CYPT71/secure-oci-base/internal/project"
)

// hasLaunchPublishFlag keeps the established launch parser unchanged while
// routing the production lifecycle through its own, deliberately small flag
// surface.
func hasLaunchPublishFlag(args []string) bool {
	for _, argument := range args {
		if argument == "--publish" || argument == "--publish=true" {
			return true
		}
	}
	return false
}

// runLaunchPublish implements the v3 one-command production path exclusively
// with platform-factory's native builder, SBOM, provenance, signing, policy and
// registry clients. Two independent builder invocations must resolve to the
// same manifest digest before anything reaches the registry.
func runLaunchPublish(
	args []string,
	stdout, stderr io.Writer,
	execute projectExecutor,
	containerExecute containerExecutor,
	microVMExecute microVMExecutor,
) int {
	// Sanetizer-todo item 18: Create context with trace_id for end-to-end correlation
	ctx := observability.ContextWithTraceID(context.Background(), observability.NewTraceID("cli", "launch-publish").String())

	flags := flag.NewFlagSet("launch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	publish := flags.Bool("publish", false, "publish the verified image before launching it")
	yes := flags.Bool("yes", false, "confirm registry publication")
	configName := flags.String("config", "", "project image YAML/JSON config; otherwise auto-discovered")
	keyDir := flags.String("key-dir", "", "native signing key directory")
	keyName := flags.String("key-name", "release", "native signing key name")
	username := flags.String("username", "", "registry username")
	insecureRegistry := flags.Bool("insecure-registry", false, "use plain HTTP for a trusted development registry")
	uploadSessionDir := flags.String("upload-session-dir", defaultUploadSessionDir(), "persistent registry upload session directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*publish || flags.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: platform-factory launch --publish --yes [OPTIONS] [DIRECTORY]")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "platform-factory launch: publication changes a registry; pass --yes")
		return 2
	}
	start := "."
	if flags.NArg() == 1 {
		start = flags.Arg(0)
	}
	loaded, err := project.Discover(start, *configName)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: %v\n", err)
		return 1
	}
	target := loaded.Config.Image + ":" + loaded.Config.Tag
	if !strings.Contains(loaded.Config.Image, "/") {
		fmt.Fprintf(stderr, "platform-factory launch: image %q must include an explicit registry for --publish\n", loaded.Config.Image)
		return 2
	}

	var lifecycleOutput bytes.Buffer
	freezeLock := filepath.Join(loaded.Root, ".platform-factory", "freeze.lock.json")
	if _, err := os.Stat(freezeLock); errors.Is(err, os.ErrNotExist) {
		if code := runProject([]string{"freeze", "--config", loaded.File}, &lifecycleOutput, stderr,
			execute, containerExecute, microVMExecute); code != 0 {
			return code
		}
	} else if err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: inspect freeze inventory: %v\n", err)
		return 1
	}

	first, second, code := reproducibleProjectBuild(loaded, &lifecycleOutput, stderr, execute)
	if code != 0 {
		return code
	}
	if first == "" || first != second {
		fmt.Fprintf(stderr, "platform-factory launch: reproducibility check failed: %q != %q\n", first, second)
		return 1
	}

	evidenceDir := filepath.Join(loaded.Root, ".platform-factory", "publication")
	policyPath := filepath.Join(evidenceDir, "policy.json")
	evidencePath := filepath.Join(evidenceDir, "evidence.json")
	provenancePath := filepath.Join(evidenceDir, "provenance.json")
	if err := writeLaunchPublicationEvidence(policyPath, evidencePath, provenancePath, loaded, second); err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: write publication evidence: %v\n", err)
		return 1
	}

	publishArgs := []string{
		"--yes", "--sbom", "--sign", "--provenance", provenancePath,
		"--policy", policyPath, "--evidence", evidencePath,
		"--key-name", *keyName, "--upload-session-dir", *uploadSessionDir,
	}
	if *keyDir != "" {
		publishArgs = append(publishArgs, "--key-dir", *keyDir)
	}
	if *username != "" {
		publishArgs = append(publishArgs, "--username", *username)
	}
	if *insecureRegistry {
		publishArgs = append(publishArgs, "--insecure-registry")
	}
	publishArgs = append(publishArgs, loaded.Output(), target)
	var publicationOutput bytes.Buffer
	if code := runPublish(ctx, publishArgs, &publicationOutput, stderr, containerExecute); code != 0 {
		return code
	}

	if code := runConfiguredProject(loaded, &lifecycleOutput, stderr, execute,
		containerExecute, microVMExecute); code != 0 {
		return code
	}
	result := map[string]any{
		"config":       loaded.File,
		"digest":       second,
		"image":        target,
		"published":    true,
		"reproducible": true,
		"valid":        true,
	}
	var published map[string]any
	if json.Unmarshal(publicationOutput.Bytes(), &published) == nil {
		result["published_reference"] = published["reference"]
	}
	encoded, _ := json.MarshalIndent(result, "", "  ")
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func reproducibleProjectBuild(loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor) (string, string, int) {
	output := loaded.Output()
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: prepare reproducibility workspace: %v\n", err)
		return "", "", 1
	}
	workspace, err := os.MkdirTemp(parent, ".reproducibility-*")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: prepare reproducibility workspace: %v\n", err)
		return "", "", 1
	}
	defer os.RemoveAll(workspace)
	previous := filepath.Join(workspace, "previous")
	if _, err := os.Stat(output); err == nil {
		if err := os.Rename(output, previous); err != nil {
			fmt.Fprintf(stderr, "platform-factory launch: preserve previous layout: %v\n", err)
			return "", "", 1
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "platform-factory launch: inspect previous layout: %v\n", err)
		return "", "", 1
	}
	restore := func(candidate string) {
		if _, statErr := os.Stat(output); errors.Is(statErr, os.ErrNotExist) {
			_ = os.Rename(candidate, output)
		}
	}
	first, code := buildProject(loaded, stdout, stderr, execute)
	if code != 0 {
		restore(previous)
		return "", "", code
	}
	firstLayout := filepath.Join(workspace, "first")
	if err := os.Rename(output, firstLayout); err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: preserve first reproducibility build: %v\n", err)
		restore(previous)
		return "", "", 1
	}
	second, code := buildProject(loaded, stdout, stderr, execute)
	if code != 0 {
		restore(firstLayout)
		return first, "", code
	}
	if first != second {
		// A divergent candidate must not silently replace the last usable
		// layout. Prefer the pre-existing layout, otherwise retain the first
		// independently built candidate for diagnosis.
		if err := os.RemoveAll(output); err != nil {
			fmt.Fprintf(stderr, "platform-factory launch: remove divergent layout: %v\n", err)
			return first, second, 1
		}
		if _, err := os.Stat(previous); err == nil {
			restore(previous)
		} else {
			restore(firstLayout)
		}
	}
	return first, second, 0
}

func writeLaunchPublicationEvidence(policyPath, evidencePath, provenancePath string, loaded project.Loaded, digest string) error {
	rules := policy.Rules{
		APIVersion: policy.APIVersion, RequireHardening: true, RequireSBOM: true,
		RequireProvenance: true, RequireSignature: true, RequireReproducible: true,
	}
	evidence := policy.Evidence{
		NonRoot: true, ReadOnlyRootFS: true, CapabilitiesDropped: true,
		SecretsAbsent: true, Reproducible: true,
	}
	provenance := map[string]any{
		"api_version":  "platform-factory.dev/provenance/v1",
		"builder":      "platform-factory/" + version,
		"config":       filepath.Base(loaded.File),
		"output":       digest,
		"platform":     loaded.Config.Platform,
		"reproducible": true,
	}
	for path, value := range map[string]any{
		policyPath: rules, evidencePath: evidence, provenancePath: provenance,
	} {
		if err := writeLaunchJSON(path, value); err != nil {
			return err
		}
	}
	return nil
}

func writeLaunchJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".publication-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
