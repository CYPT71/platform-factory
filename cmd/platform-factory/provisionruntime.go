package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/CYPT71/platform-factory/cmd/tui/runtimetui"
	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/sdk/langplugin"
)

type provisionedRuntime struct {
	Runtime string                      `json:"runtime"`
	Include []provisionedRuntimeInclude `json:"include"`
}

type provisionedRuntimeInclude struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Category    string `json:"category"`
}

// provisionRuntimeFromRoot invokes language's already-loaded plugin
// "runtime" subcommand against imageRoot - a pulled image's extracted
// filesystem, or "/" itself when a matching interpreter already on this
// host is being reused instead (see hostRuntimeCandidate) - and records
// the result into loaded's pf.yaml. Shared by the explicit `pf plugin
// provision-runtime` command and pf init's own automatic offer.
func provisionRuntimeFromRoot(loaded project.Loaded, language, imageRoot string, execute projectExecutor, stderr io.Writer) (provisionedRuntime, error) {
	binary, err := langplugin.Resolve(language)
	if err != nil {
		return provisionedRuntime{}, err
	}
	runtimeArgs := []string{"runtime", "--root", loaded.Root, "--image-root", imageRoot}
	fmt.Fprintf(stderr, "platform-factory: %s\n", formatCommand(binary, runtimeArgs))
	var manifestOut bytes.Buffer
	if err := execute(binary, runtimeArgs, loaded.Root, &manifestOut, stderr); err != nil {
		return provisionedRuntime{}, fmt.Errorf("%s runtime: %w", binary, err)
	}
	var manifest provisionedRuntime
	if err := json.Unmarshal(manifestOut.Bytes(), &manifest); err != nil {
		return provisionedRuntime{}, fmt.Errorf("decode plugin output: %w", err)
	}
	if manifest.Runtime == "" {
		return provisionedRuntime{}, errors.New("plugin reported no runtime path")
	}
	if err := appendRuntimeToConfig(loaded.File, manifest, loaded.Config.Artifact); err != nil {
		return provisionedRuntime{}, fmt.Errorf("update %s: %w", filepath.Base(loaded.File), err)
	}
	return manifest, nil
}

// runPluginProvisionRuntime is the explicit, opt-in fix for the
// "capability preflight failed... pf.yaml has no runtime field set"
// error validateBuildCapability (project.go) reports for an interpreted
// project: pull a digest-pinned base image via the project's own native
// OCI registry client (internal/registry, wrapped by pullImageRootfs -
// never the docker/podman CLI), hand the extracted filesystem to the
// project's own already-loaded language plugin (native to that language,
// per its own "runtime" subcommand - currently only plugins/lang-python
// implements one), and record the plugin's resolved runtime/include
// fields into pf.yaml. Never runs implicitly during a build - a build
// must never reach out to a registry on its own.
func runPluginProvisionRuntime(ctx context.Context, args []string, stdout, stderr io.Writer, execute projectExecutor) int {
	flags := flag.NewFlagSet("plugin provision-runtime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	language := flags.String("language", "", "language whose already-loaded plugin will provision the runtime")
	image := flags.String("image", "", "digest-pinned base image to pull the runtime from, e.g. python@sha256:...")
	dir := flags.String("dir", ".", "project directory")
	architecture := flags.String("arch", "amd64", "target architecture")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *language == "" || *image == "" {
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
	// rootfs.Convert (via pullImageRootfs) requires its own output
	// directory not to already exist yet - MkdirTemp itself already
	// creates scratchDir, so extraction targets a not-yet-existing
	// subdirectory of it instead.
	imageRootDir := filepath.Join(scratchDir, "rootfs")

	fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: pulling %s (linux/%s)\n", *image, *architecture)
	digest, err := pullImageRootfs(ctx, *image, *architecture, imageRootDir)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: pull %s: %v\n", *image, err)
		return 1
	}
	fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: resolved %s\n", digest)

	manifest, err := provisionRuntimeFromRoot(loaded, *language, imageRootDir, execute, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory plugin provision-runtime: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "provisioned runtime %s from %s@%s\n", manifest.Runtime, *image, digest)
	fmt.Fprintln(stdout, "next: pf freeze, then pf build")
	return 0
}

// appendRuntimeToConfig appends runtime/args/include YAML to the end of
// an existing pf.yaml (these fields are never already present - callers
// already refuse a project whose Config.Runtime is set). The candidate
// content is validated by round-tripping it through project.Load, the
// exact loader pf build itself uses, before it ever replaces the real
// file: a malformed append must fail here, on this command, not
// silently corrupt the project's config for the next one.
func appendRuntimeToConfig(configPath string, manifest provisionedRuntime, artifact string) error {
	existing, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var addition strings.Builder
	fmt.Fprintf(&addition, "runtime: %s\n", yamlQuoteRuntime(manifest.Runtime))
	if artifact != "" {
		// project.Config has no working_dir field, and the interpreter's
		// own process starts with no guaranteed CWD - an absolute path
		// is passed instead of the bare artifact name so it resolves
		// correctly regardless. includeProject (internal/project/
		// files.go) always places the whole project root at /app, so
		// the artifact (itself already project-root-relative) lands at
		// /app/<artifact> - verified by hand: a bare relative "main.py"
		// argument failed ("can't open file '//main.py'"), the absolute
		// form does not.
		addition.WriteString("args:\n")
		fmt.Fprintf(&addition, "  - %s\n", yamlQuoteRuntime("/app/"+artifact))
	}
	if len(manifest.Include) > 0 {
		addition.WriteString("include:\n")
		for _, entry := range manifest.Include {
			fmt.Fprintf(&addition, "  - source: %s\n    destination: %s\n    category: %s\n",
				yamlQuoteRuntime(entry.Source), yamlQuoteRuntime(entry.Destination), yamlQuoteRuntime(entry.Category))
		}
	}
	updated := append(append([]byte{}, existing...), []byte(addition.String())...)

	validation, err := os.CreateTemp(filepath.Dir(configPath), ".platform-factory-provision-runtime-validate-*")
	if err != nil {
		return err
	}
	defer os.Remove(validation.Name())
	if _, err := validation.Write(updated); err != nil {
		validation.Close()
		return err
	}
	if err := validation.Close(); err != nil {
		return err
	}
	if _, err := project.Load(validation.Name()); err != nil {
		return fmt.Errorf("resulting config would be invalid: %w", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Dir(configPath), filepath.Base(configPath), updated, info.Mode().Perm(), false)
}

func yamlQuoteRuntime(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// hostInterpreterNames maps a language to the interpreter binary name
// its plugin's "runtime" subcommand would stage from - the interpreter
// this process itself might already have installed, avoiding a
// registry pull entirely on a matching host. Extend this as more
// language plugins implement a "runtime" subcommand (currently only
// plugins/lang-python does).
var hostInterpreterNames = map[string]string{"python": "python3"}

// hostRuntimeCandidate looks for language's own interpreter already on
// PATH and returns its path only if it's a real ELF binary for
// targetArch - the only shape platform-factory's own build tooling
// could ever stage into a linux container image. Any other outcome (no
// plugin support for this language, nothing on PATH, or a binary that
// isn't a linux/targetArch ELF - a macOS/Windows host's own
// interpreter, or a mismatched architecture) reports "" rather than an
// error: the caller simply falls back to offering an image pull
// instead, the same choice that was already there.
func hostRuntimeCandidate(language, targetArch string) string {
	binaryName, ok := hostInterpreterNames[language]
	if !ok {
		return ""
	}
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return ""
	}
	file, err := elf.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	if file.Machine != elfMachineForArch(targetArch) {
		return ""
	}
	return path
}

func elfMachineForArch(arch string) elf.Machine {
	if arch == "arm64" {
		return elf.EM_AARCH64
	}
	return elf.EM_X86_64
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
	// validateBuildCapability is the exact same check `pf build` itself
	// gates on: nil here means either this language/profile never needs
	// a runtime field (a compiled language) or one is already set -
	// either way, there is nothing to offer.
	if validateBuildCapability(loaded) == nil {
		return
	}
	if _, err := langplugin.Resolve(language); err != nil {
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

	hostCandidate := hostRuntimeCandidate(language, architecture)
	choice, err := runtimetui.Confirm(language, hostCandidate, "linux/"+architecture)
	if err != nil {
		fmt.Fprintf(stderr, "platform-factory init: runtime provisioning prompt: %v\n", err)
		return
	}

	switch choice.Source {
	case runtimetui.SourceSkip:
		return
	case runtimetui.SourceHost:
		if _, err := provisionRuntimeFromRoot(loaded, language, "/", executeProjectCommand, stderr); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: provision runtime from host: %v\n", err)
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
		digest, err := pullImageRootfs(ctx, choice.Image, architecture, imageRootDir)
		if err != nil {
			fmt.Fprintf(stderr, "platform-factory init: pull %s: %v\n", choice.Image, err)
			return
		}
		if _, err := provisionRuntimeFromRoot(loaded, language, imageRootDir, executeProjectCommand, stderr); err != nil {
			fmt.Fprintf(stderr, "platform-factory init: provision runtime: %v\n", err)
			return
		}
		fmt.Fprintf(stdout, "provisioned runtime from %s@%s\n", choice.Image, digest)
	}
}
