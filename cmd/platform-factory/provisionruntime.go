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
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/CYPT71/platform-factory/cmd/tui/runtimetui"
	projectapp "github.com/CYPT71/platform-factory/internal/app/project"
	"github.com/CYPT71/platform-factory/internal/app/provisionruntime"
	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

// newProvisionRuntimeService builds a provisionruntime.Runtime wired
// with sdk/langplugin.Resolve, the one real dependency
// internal/app/provisionruntime deliberately leaves for its caller to
// supply (see New's doc comment) so that package itself has no sdk/
// dependency.
func newProvisionRuntimeService() provisionruntime.Runtime {
	return provisionruntime.New(langplugin.Resolve)
}

// runPluginProvisionRuntime is the explicit, opt-in fix for the
// "capability preflight failed... pf.yaml has no runtime field set"
// error projectapp.ValidateBuildCapability (project.go) reports for an interpreted
// project: pull a digest-pinned base image via the project's own native
// OCI registry client (internal/app/provisionruntime, wrapping
// internal/registry - never the docker/podman CLI), hand the extracted
// filesystem to the project's own already-loaded language plugin, and
// record the plugin's resolved runtime/include fields into pf.yaml.
// Never runs implicitly during a build - a build must never reach out
// to a registry on its own.
func runPluginProvisionRuntime(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plugin provision-runtime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	language := flags.String("language", "", "language whose already-loaded plugin will provision the runtime")
	image := flags.String("image", "", "digest-pinned base image to pull the runtime from, e.g. python@sha256:...")
	dir := flags.String("dir", ".", "project directory")
	architecture := flags.String("arch", "amd64", "target architecture")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *language == "" || *image == "" || !validOutputFormat(*format) {
		fmt.Fprintln(stderr, "platform-factory plugin provision-runtime: --language and --image are required")
		return 2
	}

	loaded, err := project.Discover(*dir, "")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: discover project: %v\n", err)
		return 2
	}
	if strings.TrimSpace(loaded.Config.Runtime) != "" {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: %s already has a runtime field - remove it first to re-provision\n", filepath.Base(loaded.File))
		return 2
	}

	scratchDir, err := os.MkdirTemp("", "platform-factory-provision-runtime-*")
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: %v\n", err)
		return 1
	}
	defer os.RemoveAll(scratchDir)
	// The pull's own extraction requires its output directory not to
	// already exist yet - MkdirTemp itself already creates scratchDir,
	// so extraction targets a not-yet-existing subdirectory of it
	// instead.
	imageRootDir := filepath.Join(scratchDir, "rootfs")

	svc := newProvisionRuntimeService()
	fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: pulling %s (linux/%s)\n", *image, *architecture)
	digest, err := svc.PullImageRootfs(ctx, *image, *architecture, imageRootDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: pull %s: %v\n", *image, err)
		return 1
	}
	fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: resolved %s\n", digest)

	manifest, err := svc.ProvisionFromRoot(loaded, *language, imageRootDir, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: %v\n", err)
		return 1
	}
	if err := refreshProvisionedProjectLock(loaded, &project.LockedInput{Name: *image, Digest: digest}); err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: update project lock: %v\n", err)
		return 1
	}
	if *format == "json" {
		result := struct {
			APIVersion  string `json:"api_version"`
			Operation   string `json:"operation"`
			Resource    string `json:"resource"`
			Language    string `json:"language"`
			Runtime     string `json:"runtime"`
			Image       string `json:"image"`
			ImageDigest string `json:"image_digest"`
			Status      string `json:"status"`
		}{cliOutputAPIVersion, "provision_runtime", "plugin_runtime", *language, manifest.Runtime, *image, digest, "provisioned"}
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: encode output: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "provisioned runtime %s from %s@%s\n", manifest.Runtime, *image, digest)
	fmt.Fprintln(stdout, "next: pf freeze, then pf build")
	return 0
}

// autoProvisionRuntime offers to provision language's runtime as part
// of `pf init` itself, rather than leaving the user to separately
// discover and run `pf plugin provision-runtime` with a digest-pinned
// image reference they'd already need to know. It is a pure best-
// effort enhancement, gated on a real interactive terminal: any failure
// along the way (no loaded plugin for this language, the plugin
// doesn't implement "runtime", a pull failing, ...) is reported to
// stderr and simply falls through to the same "run it manually later"
// guidance init already gives - it never fails init itself.
func autoProvisionRuntime(ctx context.Context, dir, language string, stdout, stderr io.Writer) {
	loaded, err := project.Discover(dir, "")
	if err != nil {
		return
	}
	// projectapp.ValidateBuildCapability is the exact same check `pf build` itself
	// gates on: nil here means either this language/profile never needs
	// a runtime field (a compiled language) or one is already set -
	// either way, there is nothing to offer.
	if projectapp.ValidateBuildCapability(loaded) == nil {
		return
	}
	svc := newProvisionRuntimeService()
	if _, err := svc.ResolveLanguagePlugin(language); err != nil {
		// This is the exact gap `pf build` would otherwise fail on much
		// later with no earlier warning: the language was detected but
		// its plugin isn't installed, so neither this offer nor a manual
		// `pf plugin provision-runtime` can do anything until it is.
		fmt.Fprintf(stderr, "platform-factory init: warning: %v\n", err)
		fmt.Fprintln(stderr, "  next: install the plugin, then rerun `pf init` to be offered runtime provisioning again, or set the runtime field in pf.yaml by hand - `pf build` will fail until one is provided")
		return
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return
	}
	_, architecture, ok := strings.Cut(loaded.Config.Platform, "/")
	if !ok || architecture == "" {
		architecture = "amd64"
	}

	hostCandidate := svc.ResolveHostCandidate(language, architecture)
	choice, err := runtimetui.Confirm(language, hostCandidate, "linux/"+architecture)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: runtime provisioning prompt: %v\n", err)
		return
	}

	switch choice.Source {
	case runtimetui.SourceSkip:
		return
	case runtimetui.SourceHost:
		if _, err := svc.ProvisionFromRoot(loaded, language, "/", stderr); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: provision runtime from host: %v\n", err)
			return
		}
		if err := refreshProvisionedProjectLock(loaded, nil); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: update project lock: %v\n", err)
			return
		}
		fmt.Fprintln(stdout, "provisioned runtime from the host interpreter at "+hostCandidate)
	case runtimetui.SourceImage:
		scratchDir, err := os.MkdirTemp("", "platform-factory-provision-runtime-*")
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory init: %v\n", err)
			return
		}
		defer os.RemoveAll(scratchDir)
		imageRootDir := filepath.Join(scratchDir, "rootfs")
		fmt.Fprintf(stderr, "platform-factory init: pulling %s (linux/%s)\n", choice.Image, architecture)
		digest, err := svc.PullImageRootfs(ctx, choice.Image, architecture, imageRootDir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory init: pull %s: %v\n", choice.Image, err)
			return
		}
		if _, err := svc.ProvisionFromRoot(loaded, language, imageRootDir, stderr); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: provision runtime: %v\n", err)
			return
		}
		if err := refreshProvisionedProjectLock(loaded, &project.LockedInput{Name: choice.Image, Digest: digest}); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: update project lock: %v\n", err)
			return
		}
		fmt.Fprintf(stdout, "provisioned runtime from %s@%s\n", choice.Image, digest)
	}
}

func refreshProvisionedProjectLock(loaded project.Loaded, base *project.LockedInput) error {
	lockPath := loaded.AdjacentLockPath()
	lock, err := project.LoadLock(lockPath)
	if errors.Is(err, os.ErrNotExist) || lock.Version == 1 {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(loaded.File)
	if err != nil {
		return err
	}
	lock.PlanDigest, err = project.CanonicalManifestDigest(raw)
	if err != nil {
		return err
	}
	if base != nil {
		filtered := lock.Bases[:0]
		for _, existing := range lock.Bases {
			if existing.Name != base.Name {
				filtered = append(filtered, existing)
			}
		}
		lock.Bases = append(filtered, *base)
	}
	if err := lock.Validate(); err != nil {
		return err
	}
	return atomicfile.WriteJSONSensitive(lockPath, lock)
}
