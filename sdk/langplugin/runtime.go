package langplugin

import (
	"debug/elf"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// StandardLibraryDirs lists the default ELF dynamic linker search directories
// across linux/amd64 and linux/arm64 multiarch layouts.
var StandardLibraryDirs = []string{
	"lib", "lib64", "usr/lib", "usr/lib64",
	"lib/x86_64-linux-gnu", "usr/lib/x86_64-linux-gnu",
	"lib/aarch64-linux-gnu", "usr/lib/aarch64-linux-gnu",
}

// RuntimeManifest is the JSON document emitted by `<lang-plugin> runtime`.
type RuntimeManifest struct {
	Runtime string                `json:"runtime"`
	Include []RuntimeIncludeEntry `json:"include"`
}

// RuntimeIncludeEntry describes one staged runtime file or directory tree.
type RuntimeIncludeEntry struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Category    string `json:"category"`
}

// SharedObject identifies an ELF shared object inside an extracted image root.
type SharedObject struct {
	ImagePath string
	DiskPath  string
}

// ResolvedDependency is one verified ELF dependency (PT_INTERP or DT_NEEDED).
type ResolvedDependency struct {
	ImagePath                     string
	DiskPath                      string
	OriginRelative                bool
	OriginRelativeFromInterpreter string
}

// ParseRuntimeFlags parses the common `--root`, `--image-root`, and optional
// `--interpreter` flags accepted by every language plugin's `runtime` command.
func ParseRuntimeFlags(args []string) (root, imageRoot, interpreter string, err error) {
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	r := flags.String("root", "", "project root directory")
	ir := flags.String("image-root", "", "local directory containing a pulled base image's extracted filesystem")
	hint := flags.String("interpreter", "", "container path of the interpreter inside --image-root when auto-discovery is overridden")
	if err := flags.Parse(args); err != nil {
		return "", "", "", err
	}
	if *r == "" || *ir == "" {
		return "", "", "", errors.New("--root and --image-root are required")
	}
	return *r, *ir, *hint, nil
}

// WriteRuntimeManifest encodes manifest as indented JSON to stdout.
func WriteRuntimeManifest(manifest RuntimeManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// FindSharedObjects walks dir inside imageRoot and returns every regular ELF
// shared object (*.so or *.so.*) sorted by image path.
func FindSharedObjects(dir, imageRoot string) ([]SharedObject, error) {
	var found []SharedObject
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".so") && !strings.Contains(name, ".so.") {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			targetInfo, statErr := os.Stat(p)
			if statErr != nil || !targetInfo.Mode().IsRegular() {
				return nil
			}
		} else if !info.Mode().IsRegular() {
			return nil
		}
		f, elfErr := elf.Open(p)
		if elfErr != nil {
			return nil
		}
		_ = f.Close()
		rel, relErr := filepath.Rel(imageRoot, p)
		if relErr != nil {
			return relErr
		}
		found = append(found, SharedObject{
			ImagePath: "/" + filepath.ToSlash(rel),
			DiskPath:  p,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ImagePath < found[j].ImagePath })
	return found, nil
}

// FindLibrariesByPrefix scans StandardLibraryDirs inside imageRoot for ELF
// shared libraries whose filename starts with any prefix in prefixes (for
// example "libicuuc.so." or "libssl.so."). Used for runtime-dlopen'd libraries
// not listed in static DT_NEEDED entries.
func FindLibrariesByPrefix(imageRoot string, prefixes []string) ([]SharedObject, error) {
	seen := map[string]bool{}
	var found []SharedObject
	for _, dir := range StandardLibraryDirs {
		diskDir := filepath.Join(imageRoot, dir)
		entries, err := os.ReadDir(diskDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			matched := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(name, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			diskPath := filepath.Join(diskDir, name)
			info, statErr := os.Stat(diskPath)
			if statErr != nil || !info.Mode().IsRegular() {
				continue
			}
			f, elfErr := elf.Open(diskPath)
			if elfErr != nil {
				continue
			}
			_ = f.Close()
			imagePath := "/" + dir + "/" + name
			if seen[imagePath] {
				continue
			}
			seen[imagePath] = true
			found = append(found, SharedObject{ImagePath: imagePath, DiskPath: diskPath})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ImagePath < found[j].ImagePath })
	return found, nil
}

// ProbeSharedObject verifies that seed and all of its transitive DT_NEEDED
// dependencies can be resolved inside imageRoot with a consistent ELF machine.
// It is intended for probing optional plugin/runtime modules (such as tracing
// providers or optional stdlib extensions) before staging them.
func ProbeSharedObject(imageRoot string, seed SharedObject) error {
	_, err := resolveRuntimeClosureInternal(imageRoot, "", "", []SharedObject{seed}, false, nil)
	return err
}

// ResolveRuntimeClosure computes the verified transitive PT_INTERP and
// DT_NEEDED ELF dependency closure for interpreterDiskPath plus any
// alreadyProvided shared objects (which are scanned for dependencies without
// being re-emitted as separate outputs). Missing interpreter dependencies fail
// closed; if allowOptionalSeeds is true, unresolvable dependencies of
// alreadyProvided seeds are warned to warn and skipped.
func ResolveRuntimeClosure(
	imageRoot, interpreterDiskPath, interpreterImagePath string,
	alreadyProvided []SharedObject,
	allowOptionalSeeds bool,
	warn io.Writer,
) ([]ResolvedDependency, error) {
	return resolveRuntimeClosureInternal(imageRoot, interpreterDiskPath, interpreterImagePath, alreadyProvided, allowOptionalSeeds, warn)
}

func resolveRuntimeClosureInternal(
	imageRoot, interpreterDiskPath, interpreterImagePath string,
	alreadyProvided []SharedObject,
	allowOptionalSeeds bool,
	warn io.Writer,
) ([]ResolvedDependency, error) {
	visited := map[string]bool{}
	var results []ResolvedDependency
	type queued struct {
		imagePath, diskPath           string
		originRelative                bool
		originRelativeFromInterpreter string
		isSeed                        bool
	}
	var queue []queued
	if interpreterImagePath != "" {
		visited[interpreterImagePath] = true
		queue = append(queue, queued{imagePath: interpreterImagePath, diskPath: interpreterDiskPath})
	}
	for _, seed := range alreadyProvided {
		if visited[seed.ImagePath] {
			continue
		}
		visited[seed.ImagePath] = true
		queue = append(queue, queued{imagePath: seed.ImagePath, diskPath: seed.DiskPath, isSeed: true})
	}

	var expectedMachine elf.Machine
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		file, err := elf.Open(current.diskPath)
		if err != nil {
			return nil, fmt.Errorf("parse ELF %s: %w", current.imagePath, err)
		}
		if expectedMachine == 0 {
			expectedMachine = file.Machine
		} else if file.Machine != expectedMachine {
			got := file.Machine
			file.Close()
			return nil, fmt.Errorf("ELF architecture mismatch for %s: got %s, expected %s", current.imagePath, got, expectedMachine)
		}
		interp := ""
		for _, prog := range file.Progs {
			if prog.Type == elf.PT_INTERP {
				data, readErr := io.ReadAll(io.LimitReader(prog.Open(), 4096))
				if readErr != nil {
					file.Close()
					return nil, readErr
				}
				interp = strings.TrimRight(string(data), "\x00")
			}
		}
		needed, _ := file.DynString(elf.DT_NEEDED)
		rpath, _ := file.DynString(elf.DT_RPATH)
		runpath, _ := file.DynString(elf.DT_RUNPATH)
		file.Close()

		if interp != "" && !visited[interp] {
			visited[interp] = true
			diskPath := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(interp, "/")))
			if _, err := os.Stat(diskPath); err != nil {
				return nil, fmt.Errorf("ELF interpreter %s (required by %s) not found in the pulled image", interp, current.imagePath)
			}
			results = append(results, ResolvedDependency{ImagePath: interp, DiskPath: diskPath})
			queue = append(queue, queued{imagePath: interp, diskPath: diskPath})
		}

		searchDirs := buildELFSearchDirs(rpath, runpath, filepath.Dir(current.imagePath))
		for _, soname := range needed {
			resolvedImagePath, resolvedDiskPath, viaOrigin, relFromOrigin, err := resolveELFSharedLibrary(imageRoot, soname, searchDirs)
			if err != nil {
				if current.isSeed && allowOptionalSeeds {
					if warn != nil {
						fmt.Fprintf(warn, "runtime: %s needs %s, not present in this image - skipping optional seed dependency\n", current.imagePath, soname)
					}
					continue
				}
				return nil, fmt.Errorf("resolve %s (needed by %s): %w", soname, current.imagePath, err)
			}
			if visited[resolvedImagePath] {
				continue
			}
			visited[resolvedImagePath] = true
			entry := ResolvedDependency{ImagePath: resolvedImagePath, DiskPath: resolvedDiskPath}
			if viaOrigin {
				if current.originRelative || current.isSeed {
					// Seed libraries keep their image-relative layout; preserve
					// their resolved image path directly when already inside the
					// image root rather than relocating relative to /runtime.
					if current.isSeed {
						entry.OriginRelative = false
					} else {
						return nil, fmt.Errorf("%s: $ORIGIN-relative dependency of %s has no supported destination", soname, current.imagePath)
					}
				} else {
					entry.OriginRelative = true
					entry.OriginRelativeFromInterpreter = relFromOrigin
				}
			}
			results = append(results, entry)
			queue = append(queue, queued{
				imagePath: resolvedImagePath, diskPath: resolvedDiskPath,
				originRelative: entry.OriginRelative, originRelativeFromInterpreter: entry.OriginRelativeFromInterpreter,
				isSeed: current.isSeed,
			})
		}
	}
	return results, nil
}

type originSearchDir struct {
	diskDir  string
	template string
}

func buildELFSearchDirs(rpath, runpath []string, requestingImageDir string) []originSearchDir {
	var dirs []originSearchDir
	for _, raw := range append(append([]string{}, rpath...), runpath...) {
		for _, entry := range strings.Split(raw, ":") {
			if entry == "" {
				continue
			}
			if !strings.Contains(entry, "$ORIGIN") && !strings.Contains(entry, "${ORIGIN}") {
				dirs = append(dirs, originSearchDir{diskDir: entry})
				continue
			}
			template := strings.ReplaceAll(strings.ReplaceAll(entry, "${ORIGIN}", "."), "$ORIGIN", ".")
			dirs = append(dirs, originSearchDir{
				diskDir:  filepath.ToSlash(filepath.Join(requestingImageDir, template)),
				template: filepath.ToSlash(filepath.Clean(template)),
			})
		}
	}
	return dirs
}

func resolveELFSharedLibrary(imageRoot, soname string, searchDirs []originSearchDir) (imagePath, diskPath string, viaOrigin bool, relFromOrigin string, err error) {
	for _, dir := range searchDirs {
		candidate := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(dir.diskDir, "/")), soname)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		imageRel, relErr := filepath.Rel(imageRoot, candidate)
		if relErr != nil {
			return "", "", false, "", relErr
		}
		if dir.template == "" {
			return "/" + filepath.ToSlash(imageRel), candidate, false, "", nil
		}
		return "/" + filepath.ToSlash(imageRel), candidate, true, path.Join(dir.template, soname), nil
	}
	for _, dir := range StandardLibraryDirs {
		candidate := filepath.Join(imageRoot, dir, soname)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return "/" + dir + "/" + soname, candidate, false, "", nil
		}
	}
	return "", "", false, "", fmt.Errorf("not found under %v or %v", searchDirs, StandardLibraryDirs)
}

// CopyRuntimeFile copies a single regular file (dereferencing symlinks) to dest
// with deterministic executable/readable permissions.
func CopyRuntimeFile(source, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", source)
	}
	mode := info.Mode().Perm()
	if mode&0o400 == 0 {
		mode |= 0o444
	}
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

// CopyRuntimeTree copies a directory tree from source into dest, skipping any
// excluded source paths and materializing regular-file symlinks as regular
// files so downstream symlink-free OCI layer packaging accepts every file.
func CopyRuntimeTree(source, dest string, excludedSource map[string]bool) error {
	return filepath.Walk(source, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if excludedSource[p] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, statErr := os.Stat(p)
			if statErr != nil || !resolved.Mode().IsRegular() {
				return nil
			}
			return CopyRuntimeFile(p, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return CopyRuntimeFile(p, target)
	})
}

// RelRuntimePath returns path relative to root in slash form.
func RelRuntimePath(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}
