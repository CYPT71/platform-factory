// Package provisionruntime is the application-layer service behind `pf
// plugin provision-runtime` and pf init's automatic runtime-provisioning
// offer: it stages a language interpreter's runtime closure - either
// pulled from a digest-pinned base image, or reused from a matching
// interpreter already on the host - into a project's pf.yaml.
// cmd/platform-factory/provisionruntime.go now only parses flags,
// handles the interactive TUI prompt, and calls Runtime methods; every
// actual pull/plugin-invocation/config-mutation step lives here, where
// it can be tested without going through the CLI or a real terminal.
package provisionruntime

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CYPT71/platform-factory/internal/atomicfile"
	"github.com/CYPT71/platform-factory/internal/project"
	"github.com/CYPT71/platform-factory/internal/shellquote"
)

// Manifest is what a language plugin's "runtime" subcommand reports:
// the resolved runtime interpreter path and the files it needs staged
// alongside it.
type Manifest struct {
	Runtime string            `json:"runtime"`
	Include []ManifestInclude `json:"include"`
}

type ManifestInclude struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Category    string `json:"category"`
}

// Runtime is the narrow contract cmd/platform-factory depends on for
// runtime provisioning: resolving a matching interpreter already on the
// host, pulling a digest-pinned base image's rootfs, resolving whether
// a language's plugin is loaded at all, and invoking that plugin's
// "runtime" subcommand to provision + record the result into pf.yaml.
// cmd only ever holds a Runtime value, never the concrete type behind
// it, so it can't reach past this API to mutate a dependency mid-use.
type Runtime interface {
	ResolveHostCandidate(language, targetArch string) string
	PullImageRootfs(ctx context.Context, imageRef, architecture, destDir string) (string, error)
	ResolveLanguagePlugin(language string) (string, error)
	ProvisionFromRoot(loaded project.Loaded, language, imageRoot string, stderr io.Writer) (Manifest, error)
}

// service is Runtime's only implementation, its dependencies unexported
// so nothing outside this package can reach past New's wiring to swap
// one mid-use - a test that needs a fake dependency constructs a
// service literal directly (same package, unexported fields still
// visible).
type service struct {
	execute           func(name string, args []string, directory string, stdout, stderr io.Writer) error
	pullImageRootfs   func(ctx context.Context, imageRef, architecture, destDir string) (string, error)
	resolveLangplugin func(language string) (string, error)
	lookPath          func(file string) (string, error)
	openELFMachine    func(path string) (elf.Machine, error)
}

// New wires every dependency to its real implementation.
// resolveLangplugin must be sdk/langplugin.Resolve: this package has no
// sdk/ dependency itself, so its caller supplies that one piece at
// construction, as a required parameter rather than a field a caller
// could forget to set - a missing wiring is now a compile error at the
// call site, not a nil-func panic the first time it's used.
func New(resolveLangplugin func(language string) (string, error)) Runtime {
	return &service{
		execute:           executeCommand,
		pullImageRootfs:   pullImageRootfs,
		resolveLangplugin: resolveLangplugin,
		lookPath:          exec.LookPath,
		openELFMachine: func(path string) (elf.Machine, error) {
			file, err := elf.Open(path)
			if err != nil {
				return 0, err
			}
			defer file.Close()
			return file.Machine, nil
		},
	}
}

// ResolveLanguagePlugin reports whether language's plugin is loaded,
// resolving to its binary path - the same resolution ProvisionFromRoot
// uses internally, exposed directly for pf init's own preflight check
// (offering runtime provisioning only when the plugin is actually
// available).
func (s *service) ResolveLanguagePlugin(language string) (string, error) {
	return s.resolveLangplugin(language)
}

func executeCommand(name string, args []string, directory string, stdout, stderr io.Writer) error {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

// ProvisionFromRoot invokes language's already-loaded plugin "runtime"
// subcommand against imageRoot - a pulled image's extracted filesystem,
// or "/" itself when a matching interpreter already on this host is
// being reused instead (see ResolveHostCandidate) - and records the
// result into loaded's pf.yaml. Shared by the explicit `pf plugin
// provision-runtime` command and pf init's own automatic offer.
func (s *service) ProvisionFromRoot(loaded project.Loaded, language, imageRoot string, stderr io.Writer) (Manifest, error) {
	binary, err := s.resolveLangplugin(language)
	if err != nil {
		return Manifest{}, err
	}
	runtimeArgs := []string{"runtime", "--root", loaded.Root, "--image-root", imageRoot}
	fmt.Fprintf(stderr, "platform-factory: %s\n", shellquote.Command(binary, runtimeArgs))
	var manifestOut bytes.Buffer
	if err := s.execute(binary, runtimeArgs, loaded.Root, &manifestOut, stderr); err != nil {
		return Manifest{}, fmt.Errorf("%s runtime: %w", binary, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestOut.Bytes(), &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode plugin output: %w", err)
	}
	if manifest.Runtime == "" {
		return Manifest{}, errors.New("plugin reported no runtime path")
	}
	if err := appendRuntimeToConfig(loaded.File, manifest, loaded.Config.Artifact); err != nil {
		return Manifest{}, fmt.Errorf("update %s: %w", filepath.Base(loaded.File), err)
	}
	return manifest, nil
}

// appendRuntimeToConfig appends runtime/args/include YAML to the end of
// an existing pf.yaml (these fields are never already present - callers
// already refuse a project whose Config.Runtime is set). The candidate
// content is validated by round-tripping it through project.Load, the
// exact loader pf build itself uses, before it ever replaces the real
// file: a malformed append must fail here, on this command, not
// silently corrupt the project's config for the next one.
func appendRuntimeToConfig(configPath string, manifest Manifest, artifact string) error {
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

// ResolveHostCandidate looks for language's own interpreter already on
// PATH and returns its path only if it's a real ELF binary for
// targetArch - the only shape platform-factory's own build tooling
// could ever stage into a linux container image. Any other outcome (no
// plugin support for this language, nothing on PATH, or a binary that
// isn't a linux/targetArch ELF - a macOS/Windows host's own
// interpreter, or a mismatched architecture) reports "" rather than an
// error: the caller simply falls back to offering an image pull
// instead.
func (s *service) ResolveHostCandidate(language, targetArch string) string {
	binaryName, ok := hostInterpreterNames[language]
	if !ok {
		return ""
	}
	path, err := s.lookPath(binaryName)
	if err != nil {
		return ""
	}
	machine, err := s.openELFMachine(path)
	if err != nil {
		return ""
	}
	if machine != elfMachineForArch(targetArch) {
		return ""
	}
	return path
}

// PullImageRootfs pulls imageRef via the project's own native OCI
// registry client and extracts it into destDir.
func (s *service) PullImageRootfs(ctx context.Context, imageRef, architecture, destDir string) (string, error) {
	return s.pullImageRootfs(ctx, imageRef, architecture, destDir)
}

func elfMachineForArch(arch string) elf.Machine {
	if arch == "arm64" {
		return elf.EM_AARCH64
	}
	return elf.EM_X86_64
}
