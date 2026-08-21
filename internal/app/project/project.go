// Package project is the application-layer service behind `pf project`'s
// self-contained business rules: whether a built layout needs a
// rebuild, whether a project has real dependencies to pin, validating
// its build DAG and interpreted-runtime capability preflight, mapping a
// language to its runtime profile, and deriving a stable watch
// container name. cmd/platform-factory/project.go's own CLI-facing
// orchestration (flag parsing, the plugin-host/TUI/executor-heavy
// dispatch across show/plan/freeze/build/run/launch/migrate/watch)
// stays there - most of it is too entangled with other CLI-only types
// (*pluginHost, projectExecutor, buildtui) to extract safely in the
// same pass; only the pieces that operate on project.Loaded alone live
// here, where they can be tested without going through the CLI at all.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CYPT71/platform-factory/internal/pipeline"
	"github.com/CYPT71/platform-factory/internal/project"
)

// maxRebuildStatFiles bounds NeedsRebuild's staleness walk over a
// bundled directory source (e.g. the project's own tree, or a shared
// dependency tree) - the same order of magnitude as this codebase's
// other file-count budgets (internal/layout's maxArchiveFiles). Staleness
// detection is a development convenience, not a correctness boundary, so
// once the cap is hit it stops looking rather than walking an arbitrarily
// large tree on every `pf run`.
const maxRebuildStatFiles = 10000

// NeedsRebuild reports whether loaded's built layout is missing or
// older than any of its real inputs: the project config itself, the
// built artifact/runtime binary, and every source ImageFiles() would
// bundle. It compares against index.json's own mtime rather than the
// output directory's, since that file is guaranteed to be rewritten by
// every successful build regardless of what else changed inside the
// layout tree.
func NeedsRebuild(loaded project.Loaded) (bool, error) {
	indexInfo, err := os.Stat(filepath.Join(loaded.Output(), "index.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	builtAt := indexInfo.ModTime()

	binaryName := loaded.Config.Artifact
	if loaded.Config.Runtime != "" {
		binaryName = loaded.Config.Runtime
	}
	sources := []string{loaded.File, loaded.Resolve(binaryName)}
	files, err := loaded.ImageFiles()
	if err != nil {
		return false, err
	}
	for _, file := range files {
		sources = append(sources, file.Source)
	}

	remaining := maxRebuildStatFiles
	for _, source := range sources {
		stale, err := sourceNewerThan(source, builtAt, &remaining)
		if err != nil {
			return false, err
		}
		if stale {
			return true, nil
		}
		if remaining <= 0 {
			break
		}
	}
	return false, nil
}

// sourceNewerThan reports whether path (a file, or every regular file
// under a directory) has a modification time after builtAt. A path that
// no longer exists is not itself a reason to rebuild - the normal build
// path already reports a clear error for a genuinely missing artifact.
func sourceNewerThan(path string, builtAt time.Time, remaining *int) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	if !info.IsDir() {
		*remaining--
		return info.ModTime().After(builtAt), nil
	}
	stale := false
	walkErr := filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if *remaining <= 0 {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		*remaining--
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if entryInfo.ModTime().After(builtAt) {
			stale = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return false, nil
	}
	return stale, nil
}

// RequiresFrozenInputs reports whether loaded's build path checks
// VerifyFreezeInventory before proceeding - true whenever there are
// real dependencies to pin (explicit includes/shared deps, or a
// dependency-management mode other than none/external).
func RequiresFrozenInputs(loaded project.Loaded) bool {
	if len(loaded.Config.Include) > 0 || len(loaded.Config.SharedDeps) > 0 {
		return true
	}
	dependencies := loaded.Config.DependencyManagement
	return dependencies != nil && dependencies.Mode != "none" && dependencies.Mode != "external"
}

// ValidateBuildDAG validates loaded's build pipeline DAG: an explicit
// .pf/build.pipeline.json if present, otherwise the DAG loaded.Pipeline
// derives from the config itself.
func ValidateBuildDAG(loaded project.Loaded) error {
	filename := filepath.Join(loaded.Root, ".pf", "build.pipeline.json")
	info, err := os.Lstat(filename)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New(".pf/build.pipeline.json must be a regular file, not a symlink")
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer file.Close()
		_, _, err = pipeline.Decode(file)
		return err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	definition, err := loaded.Pipeline(nil)
	if err != nil {
		return err
	}
	_, err = pipeline.Analyze(definition)
	return err
}

// ValidateBuildCapability is the compatibility facade for callers that only
// need a verdict. AssessBuildCapabilities is the authoritative structured
// probe used by pf build and also exposes deferred run-time capabilities.
func ValidateBuildCapability(loaded project.Loaded) error {
	_, err := AssessBuildCapabilities(context.Background(), loaded)
	return err
}

// Profile maps a project language to a runtime profile. Go, Rust and
// other compiled languages deliberately map to "static": they produce
// ELF executables, and the ELF detection in oci picks
// static/glibc/musl from the actual binary rather than from the
// language name.
func Profile(language string) string {
	switch strings.ToLower(language) {
	case "python":
		return "python"
	case "node", "nodejs", "javascript", "typescript":
		return "node"
	case "java":
		return "java"
	case "dotnet", "csharp", "fsharp":
		return "dotnet"
	case "ruby":
		return "ruby"
	case "php":
		return "php"
	default:
		return "static"
	}
}

// WatchContainerName derives a stable docker/podman container name from
// the project directory, so repeated rebuild/restart cycles always
// replace the same named container rather than accumulating new ones.
func WatchContainerName(loaded project.Loaded) string {
	var b strings.Builder
	for _, r := range filepath.Base(loaded.Root) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "project"
	}
	return "pf-watch-" + name
}
