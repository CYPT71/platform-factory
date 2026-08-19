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

	buildapp "github.com/CYPT71/platform-factory/internal/app/build"
	"github.com/CYPT71/platform-factory/internal/project"
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
	ctx := commandContext(context.Background(), "launch-publish")

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
		if code := runProjectContext(ctx, []string{"freeze", "--config", loaded.File}, &lifecycleOutput, stderr,
			execute, containerExecute, microVMExecute); code != 0 {
			return code
		}
	} else if err != nil {
		fmt.Fprintf(stderr, "platform-factory launch: inspect freeze inventory: %v\n", err)
		return 1
	}

	first, second, code := reproducibleProjectBuildContext(ctx, loaded, &lifecycleOutput, stderr, execute)
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

	// plugins is nil here: this path's freeze inventory was already
	// verified fresh earlier in this same function (see "inspect freeze
	// inventory" above), so rebuildProjectLayout's auto-freeze fallback
	// is not expected to trigger - resolveFreezeSteps tolerates a nil
	// plugin host and reports a clear error instead of panicking if it
	// somehow does.
	if code := runConfiguredProject(ctx, loaded, nil, &lifecycleOutput, stderr, execute,
		containerExecute, microVMExecute); code != 0 {
		return code
	}
	result := map[string]any{
		"api_version":  cliOutputAPIVersion,
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
	return reproducibleProjectBuildContext(context.Background(), loaded, stdout, stderr, execute)
}

func reproducibleProjectBuildContext(ctx context.Context, loaded project.Loaded, stdout, stderr io.Writer, execute projectExecutor) (string, string, int) {
	first, second, err := buildapp.ReproducibleBuild(loaded, func() (string, error) {
		digest, code := buildProjectContext(ctx, loaded, stdout, stderr, execute)
		if code != 0 {
			return "", &buildFailureCode{code: code}
		}
		return digest, nil
	})
	if err != nil {
		var failed *buildapp.FailedBuild
		if errors.As(err, &failed) {
			var code *buildFailureCode
			if errors.As(failed.Err, &code) {
				return first, second, code.code
			}
		}
		fmt.Fprintf(stderr, "platform-factory launch: %v\n", err)
		return first, second, 1
	}
	return first, second, 0
}

// buildFailureCode carries buildProjectContext's exact exit code (which
// has already printed its own detailed stderr message) back out through
// buildapp.ReproducibleBuild's FailedBuild wrapper, so a build failure
// still reports its original code (1 or 2) instead of a generic one.
type buildFailureCode struct{ code int }

func (e *buildFailureCode) Error() string { return fmt.Sprintf("build failed (exit %d)", e.code) }

func writeLaunchPublicationEvidence(policyPath, evidencePath, provenancePath string, loaded project.Loaded, digest string) error {
	return buildapp.WriteLaunchPublicationEvidence(policyPath, evidencePath, provenancePath, loaded, digest, version)
}
