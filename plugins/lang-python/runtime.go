package main

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
	"strings"
)

// canonicalInterpreterName is the entrypoint basename the host's own
// profile validation requires for a "python" profile (see
// internal/oci/buildconfig.go's Validate - the plugin can't import that
// package, so this is stated here as the one piece of cross-cutting
// knowledge this plugin must independently keep in sync with).
const canonicalInterpreterName = "python3"

// standardLibraryDirs is where the ELF dynamic linker finds a shared
// library when no RPATH/RUNPATH entry resolves it - the ld.so.cache /
// default search path, which is location-independent (the destination
// for a library found this way is its own real absolute path from the
// source image, not relocated). Scoped to the one architecture this pass
// targets (see the plan's explicit scope note); a future non-amd64 pass
// would need its own triplet.
var standardLibraryDirs = []string{
	"lib", "lib64", "usr/lib", "usr/lib64",
	"lib/x86_64-linux-gnu", "usr/lib/x86_64-linux-gnu",
}

type runtimeManifest struct {
	Runtime string                `json:"runtime"`
	Include []runtimeIncludeEntry `json:"include"`
}

type runtimeIncludeEntry struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Category    string `json:"category"`
}

func runRuntime(args []string) error {
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	root := flags.String("root", "", "project root directory")
	imageRoot := flags.String("image-root", "", "local directory containing a pulled base image's extracted filesystem")
	interpreterHint := flags.String("interpreter", "", "container path of the interpreter inside --image-root, when it can't be auto-discovered (e.g. /usr/local/bin/python3.12)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *imageRoot == "" {
		return errors.New("--root and --image-root are required")
	}

	interpreterImagePath, err := findInterpreter(*imageRoot, *interpreterHint)
	if err != nil {
		return err
	}
	interpreterDiskPath := filepath.Join(*imageRoot, filepath.FromSlash(strings.TrimPrefix(interpreterImagePath, "/")))

	stdlibImagePath, stdlibDiskPath, err := findStandardLibrary(*imageRoot, interpreterImagePath)
	if err != nil {
		return fmt.Errorf("locate Python standard library: %w", err)
	}
	// internal/oci's own build validation checks every staged ELF's
	// DT_NEEDED is actually satisfied - verified by hand that this
	// includes lib-dynload's C extension modules (_hashlib.so,
	// _sqlite3.so, and the like), which have real external dependencies
	// (libcrypt, libssl, libsqlite3, ...) of their own, not just the
	// interpreter's direct ones. Seed the same closure walk with every
	// .so under the standard library so those get discovered too.
	extensionModules, err := findSharedObjects(stdlibDiskPath, *imageRoot)
	if err != nil {
		return fmt.Errorf("scan standard library for extension modules: %w", err)
	}

	// A slim base image deliberately omits native libraries whole
	// categories of extension module need (Tk/Tcl for _tkinter,
	// unixODBC for the database modules, and so on) - the .so is present
	// but was already unloadable in the original image; Python only
	// fails on it if the user's code actually imports that specific
	// module. internal/oci's own build validator has no such lazy-import
	// concept though: it checks every staged ELF's dependencies are
	// satisfied, unconditionally. So an extension module that can't
	// fully resolve is excluded from the stdlib copy entirely (probed
	// independently here, one at a time) rather than staged incomplete -
	// verified by hand: staging _tkinter.so with its two missing
	// Tcl/Tk libraries unresolved made `pf build` itself refuse the
	// image outright ("ELF dependency libtk8.6.so is required but was
	// not provided"), not just a run-time-only concern.
	excludedModules := map[string]bool{}
	var usableModules []struct{ imagePath, diskPath string }
	for _, module := range extensionModules {
		if _, probeErr := resolveRuntimeClosure(*imageRoot, module.diskPath, module.imagePath, nil); probeErr != nil {
			fmt.Fprintf(os.Stderr, "platform-factory-lang-python: runtime: excluding %s from the staged runtime (%v) - unaffected code importing other standard-library modules is unaffected\n", module.imagePath, probeErr)
			excludedModules[module.diskPath] = true
			continue
		}
		usableModules = append(usableModules, module)
	}

	dependencies, err := resolveRuntimeClosure(*imageRoot, interpreterDiskPath, interpreterImagePath, usableModules)
	if err != nil {
		return fmt.Errorf("resolve runtime dependencies of %s: %w", interpreterImagePath, err)
	}

	stageDir := filepath.Join(*root, depsRelPath, "runtime")
	if err := os.RemoveAll(stageDir); err != nil {
		return err
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}

	manifest := runtimeManifest{}
	stagedInterpreter := filepath.Join(stageDir, canonicalInterpreterName)
	if err := copyFile(interpreterDiskPath, stagedInterpreter); err != nil {
		return fmt.Errorf("stage interpreter: %w", err)
	}
	manifest.Runtime = mustRel(*root, stagedInterpreter)

	for index, dependency := range dependencies {
		stagedName := fmt.Sprintf("lib-%d-%s", index, filepath.Base(dependency.imagePath))
		stagedPath := filepath.Join(stageDir, stagedName)
		if err := copyFile(dependency.diskPath, stagedPath); err != nil {
			return fmt.Errorf("stage %s: %w", dependency.imagePath, err)
		}
		destination := dependency.imagePath
		if dependency.originRelative {
			// This dependency was only found via a $ORIGIN-relative
			// RPATH/RUNPATH on the interpreter (or one of its own
			// dependencies) as originally laid out in the source image -
			// once staged at /runtime/<canonicalInterpreterName>, the
			// equivalent destination is the same relative hop computed
			// from /runtime instead of the dependency's original
			// directory, so the dynamic linker finds it in the same
			// *relative* place it always expected.
			destination = filepath.ToSlash(filepath.Join("/runtime", dependency.originRelativeFromInterpreter))
		}
		manifest.Include = append(manifest.Include, runtimeIncludeEntry{
			Source: mustRel(*root, stagedPath), Destination: destination, Category: "toolchain",
		})
	}

	// The ELF closure above gets the interpreter *running*; CPython also
	// needs its own standard library (a tree of real .py/.pyc files, not
	// linked in as a shared object at all - a JVM/CLR runtime would have
	// an analogous, differently-shaped requirement, which is exactly why
	// this is language-specific plugin logic, not something the shared
	// ELF-closure code above could ever discover). Verified this is
	// exactly what was still missing by hand: staged only the ELF closure
	// once, launched the built image, and CPython failed with
	// "ModuleNotFoundError: No module named 'encodings'" hunting for it.
	stagedStdlib := filepath.Join(stageDir, "stdlib")
	if err := copyTree(stdlibDiskPath, stagedStdlib, excludedModules); err != nil {
		return fmt.Errorf("stage standard library: %w", err)
	}
	// CPython computes sys.prefix independently of the interpreter
	// executable's own final location (confirmed by hand: moved to
	// /runtime/python3, it still reported sys.base_prefix "/usr/local") -
	// so the standard library keeps its real, original absolute image
	// path rather than being relocated like an $ORIGIN-relative library.
	manifest.Include = append(manifest.Include, runtimeIncludeEntry{
		Source: mustRel(*root, stagedStdlib), Destination: stdlibImagePath, Category: "toolchain",
	})

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// findSharedObjects walks dir (a real, already-extracted local
// directory) for every *.so file - CPython's C extension modules, mainly
// under lib-dynload - and returns each as an (image path, disk path)
// pair relative to imageRoot, ready to seed resolveRuntimeClosure.
func findSharedObjects(dir, imageRoot string) ([]struct{ imagePath, diskPath string }, error) {
	var found []struct{ imagePath, diskPath string }
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".so") {
			return nil
		}
		relative, err := filepath.Rel(imageRoot, path)
		if err != nil {
			return err
		}
		found = append(found, struct{ imagePath, diskPath string }{
			imagePath: "/" + filepath.ToSlash(relative), diskPath: path,
		})
		return nil
	})
	return found, err
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// findStandardLibrary locates CPython's own standard library directory
// (a real tree of .py/.pyc files - not an ELF dependency) at
// <prefix>/lib/python<major>.<minor>, derived from the interpreter's own
// image path: interpreterImagePath "/usr/local/bin/python3.12" has
// prefix "/usr/local" (its bin directory's parent) and version "3.12"
// (its own name suffix) - matching CPython's own installation layout
// convention, which every build of the standard python:*-slim images
// follows.
func findStandardLibrary(imageRoot, interpreterImagePath string) (imagePath, diskPath string, err error) {
	name := filepath.Base(interpreterImagePath)
	version, ok := strings.CutPrefix(name, "python3")
	if !ok {
		return "", "", fmt.Errorf("cannot derive a version from interpreter name %q", name)
	}
	prefix := filepath.Dir(filepath.Dir(interpreterImagePath)) // .../bin/pythonX.Y -> .../bin -> prefix
	stdlibImagePath := filepath.ToSlash(filepath.Join(prefix, "lib", "python3"+version))
	stdlibDiskPath := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(stdlibImagePath, "/")))
	info, statErr := os.Stat(stdlibDiskPath)
	if statErr != nil || !info.IsDir() {
		return "", "", fmt.Errorf("expected a standard library directory at %s (derived from interpreter %s), found none", stdlibImagePath, interpreterImagePath)
	}
	return stdlibImagePath, stdlibDiskPath, nil
}

// copyTree recursively copies source (a real, already-extracted local
// directory - internal/rootfs.Convert has already resolved and rejected
// any unsafe symlink on the way in) into dest.
// copyTree recursively copies source into dest, skipping any path present
// in excludedSource (keyed by the source-side absolute path, as returned
// by findSharedObjects) - the caller's way of leaving an unloadable
// extension module out of the staged runtime entirely.
func copyTree(source, dest string, excludedSource map[string]bool) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if excludedSource[path] {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(source, dest string) error {
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
	output, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()|0o100)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

// findInterpreter looks for a python3* executable under the container's
// conventional interpreter directories, preferring the most
// version-specific real (non-symlink) name - python:*-slim images ship
// e.g. both "python3" (a symlink) and "python3.12" (the real ELF binary);
// staging the real binary avoids depending on symlink handling for the
// entrypoint itself.
func findInterpreter(imageRoot, hint string) (string, error) {
	if hint != "" {
		disk := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(hint, "/")))
		if info, err := os.Stat(disk); err == nil && info.Mode().IsRegular() {
			return "/" + strings.TrimPrefix(hint, "/"), nil
		}
		return "", fmt.Errorf("--interpreter %s not found (or not a regular file) in the pulled image", hint)
	}
	for _, dir := range []string{"usr/local/bin", "usr/bin"} {
		entries, err := os.ReadDir(filepath.Join(imageRoot, dir))
		if err != nil {
			continue
		}
		var best string
		for _, entry := range entries {
			name := entry.Name()
			if !isInterpreterBinaryName(name) {
				continue
			}
			info, err := os.Stat(filepath.Join(imageRoot, dir, name))
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			// Prefer the most version-specific real name ("python3.12"
			// over "python3") - it's more likely to be the actual ELF
			// binary rather than a symlink alias, and among only names
			// this strict pattern already accepts, "longer" reliably
			// means "more specific version," not "a different utility
			// with a python3-prefixed name" (isInterpreterBinaryName
			// already excludes those, e.g. "python3.12-config").
			if len(name) > len(best) {
				best = name
			}
		}
		if best != "" {
			return "/" + dir + "/" + best, nil
		}
	}
	return "", errors.New("no python3 interpreter found under usr/local/bin or usr/bin in the pulled image - pass --interpreter to name one explicitly")
}

// isInterpreterBinaryName matches exactly "python3" or "python3.<digits>"
// (optionally with more ".<digits>" version components) - the real
// interpreter's own name, never a same-prefixed utility that ships beside
// it (python3.12-config, python3-config, 2to3, idle3, ...).
func isInterpreterBinaryName(name string) bool {
	rest, ok := strings.CutPrefix(name, "python3")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	if rest[0] != '.' {
		return false
	}
	for _, segment := range strings.Split(rest[1:], ".") {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

type resolvedDependency struct {
	imagePath      string
	diskPath       string
	originRelative bool
	// originRelativeFromInterpreter is the path this dependency was found
	// at, relative to the *directory containing the file that needed it*
	// (its immediate parent in the resolution chain) when originRelative
	// is true - joined against "/runtime" (where the interpreter itself
	// always lands) to compute the final destination. For the common
	// one-hop case (a library the interpreter itself needs via
	// $ORIGIN/../lib), this is simply that RPATH-relative path.
	originRelativeFromInterpreter string
}

// resolveRuntimeClosure walks the ELF dependency graph starting at the
// interpreter (interpreterDiskPath/interpreterImagePath, both already
// resolved and not themselves included in the result - the caller stages
// the interpreter separately) via stdlib debug/elf: PT_INTERP (the
// dynamic linker itself) and DT_NEEDED (direct shared library
// dependencies), applied recursively until no new dependency appears.
// For each DT_NEEDED entry, DT_RPATH/DT_RUNPATH on the *requesting* file
// are checked first ($ORIGIN substituted for that file's own directory);
// if none resolve it, standardLibraryDirs is searched instead and the
// dependency's real, unmodified absolute image path is kept - this is
// the exact distinction verified by hand: libpython on a python:*-slim
// image is $ORIGIN-relative, libc/libm/the ELF interpreter are found via
// the default search path and keep their real absolute location.
// alreadyProvided files are explored for their own DT_NEEDED (a C
// extension module's real, external library dependencies - libcrypt,
// libssl, libsqlite3, zlib and the like, none of which internal/oci's
// own ELF-completeness check will let slide) but never themselves added
// to the returned closure: the caller already stages them as part of a
// larger tree copy (the standard library's lib-dynload directory), and
// adding them again as individual entries would just duplicate content
// already covered.
func resolveRuntimeClosure(imageRoot, interpreterDiskPath, interpreterImagePath string, alreadyProvided []struct{ imagePath, diskPath string }) ([]resolvedDependency, error) {
	visited := map[string]bool{interpreterImagePath: true}
	var results []resolvedDependency
	type queued struct {
		imagePath, diskPath           string
		originRelative                bool
		originRelativeFromInterpreter string
		// isSeed marks a file from alreadyProvided: it keeps its own
		// real absolute image location regardless of where the
		// interpreter ends up, so an $ORIGIN-relative dependency
		// discovered from *it* cannot be relocated relative to
		// /runtime the way one discovered from the interpreter's own
		// chain can - see the guard below.
		isSeed bool
	}
	queue := []queued{{imagePath: interpreterImagePath, diskPath: interpreterDiskPath}}
	for _, seed := range alreadyProvided {
		visited[seed.imagePath] = true
		queue = append(queue, queued{imagePath: seed.imagePath, diskPath: seed.diskPath, isSeed: true})
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		file, err := elf.Open(current.diskPath)
		if err != nil {
			return nil, fmt.Errorf("parse ELF %s: %w", current.imagePath, err)
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
			// The kernel's own loader always resolves PT_INTERP as an
			// absolute path - never $ORIGIN-relative, regardless of
			// where the requesting binary itself ends up.
			results = append(results, resolvedDependency{imagePath: interp, diskPath: diskPath})
			queue = append(queue, queued{imagePath: interp, diskPath: diskPath})
		}

		searchDirs := buildSearchDirs(rpath, runpath, filepath.Dir(current.imagePath))
		for _, soname := range needed {
			// Deliberately not checked-and-skipped by the raw soname
			// string before resolving: glibc's libc.so.6 lists
			// "ld-linux-x86-64.so.2" as a DT_NEEDED soname (its own
			// directly-executable trampoline feature), the same file
			// PT_INTERP already names by full path - two different
			// strings for one file, only reconciled once both are
			// resolved to the same real image path below.
			resolvedImagePath, resolvedDiskPath, viaOrigin, relFromOrigin, err := resolveSharedLibrary(imageRoot, soname, searchDirs)
			if err != nil {
				if current.isSeed {
					// current is a lib-dynload C extension module (or a
					// dependency reached only through one) - Python
					// imports these lazily, one at a time, only when the
					// user's code actually asks for that specific
					// standard-library module. A slim base image
					// deliberately omits native libraries entire
					// categories of extension never need (Tk/Tcl for
					// _tkinter, unixodbc for the database modules, and so
					// on) - the extension .so is present but
					// unloadable, exactly as it already was in the
					// original image. Only the interpreter's own direct
					// chain (never marked isSeed) is a hard requirement;
					// an unresolvable dependency here is real but
					// pre-existing, not something this command
					// introduced, so it's reported and skipped rather
					// than failing the whole provisioning.
					fmt.Fprintf(os.Stderr, "platform-factory-lang-python: runtime: %s needs %s, not present in this image - that extension module will be unavailable, matching the original image's own behavior\n", current.imagePath, soname)
					continue
				}
				return nil, fmt.Errorf("resolve %s (needed by %s): %w", soname, current.imagePath, err)
			}
			if visited[resolvedImagePath] {
				continue
			}
			visited[resolvedImagePath] = true
			entry := resolvedDependency{imagePath: resolvedImagePath, diskPath: resolvedDiskPath}
			if viaOrigin {
				if current.originRelative || current.isSeed {
					// A transitive $ORIGIN-relative dependency of a file
					// that was itself already $ORIGIN-relative to the
					// interpreter (or of a seed file, which keeps its own
					// fixed real location and was never going to move to
					// /runtime in the first place) would need computing a
					// destination other than "relative to /runtime" -
					// cases the real python:*-slim image this was
					// verified against never exercises (its own
					// transitive deps - libc, libm, and every lib-dynload
					// module's own needs - all resolve via the standard
					// search path, not RPATH). Rather than silently
					// compute an unverified destination, fail loudly: a
					// real occurrence of this needs its own verified fix,
					// not a guess.
					return nil, fmt.Errorf("%s: $ORIGIN-relative dependency of %s has no supported destination", soname, current.imagePath)
				}
				entry.originRelative = true
				entry.originRelativeFromInterpreter = relFromOrigin
			}
			results = append(results, entry)
			queue = append(queue, queued{
				imagePath: resolvedImagePath, diskPath: resolvedDiskPath,
				originRelative: entry.originRelative, originRelativeFromInterpreter: entry.originRelativeFromInterpreter,
				isSeed: current.isSeed,
			})
		}
	}
	return results, nil
}

func splitSearchPath(value string) []string {
	if value == "" {
		return nil
	}
	var dirs []string
	for _, entry := range strings.Split(value, ":") {
		if entry != "" {
			dirs = append(dirs, entry)
		}
	}
	return dirs
}

// originSearchDir is one RPATH/RUNPATH entry, kept in two independent
// forms rather than one pre-joined string: diskDir is an image-rooted
// path usable to check whether a file actually exists there (with
// $ORIGIN substituted for requestingImageDir, e.g.
// "/usr/local/bin/../lib"); template is the *original*, unexpanded
// path relative to $ORIGIN itself (e.g. "../lib", with the literal
// "$ORIGIN"/"${ORIGIN}" prefix stripped instead of substituted) - the
// hop to actually preserve once the requesting file relocates to
// /runtime. Joining diskDir's own two path fragments together would let
// filepath.Join's automatic ".." cleanup silently cancel the very hop
// template needs to keep, so the two must never be derived from one
// another.
type originSearchDir struct {
	diskDir  string
	template string // empty for a plain (non-$ORIGIN) absolute RPATH entry
}

// buildSearchDirs turns rpath/runpath (as returned by DynString) into
// search directories for the file located at requestingImageDir, ready
// for resolveSharedLibrary.
func buildSearchDirs(rpath, runpath []string, requestingImageDir string) []originSearchDir {
	var dirs []originSearchDir
	for _, raw := range append(append([]string{}, rpath...), runpath...) {
		for _, entry := range splitSearchPath(raw) {
			if !strings.Contains(entry, "$ORIGIN") {
				// A plain absolute RPATH entry (no $ORIGIN) is not
				// requester-relative - it names the same real location
				// regardless of where the requester ends up.
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

// resolveSharedLibrary finds soname on disk within imageRoot, first among
// searchDirs (the requesting file's own RPATH/RUNPATH), then in
// standardLibraryDirs. Returns whether the match came from a
// $ORIGIN-relative entry and, if so, the path relative to the requesting
// file's own directory (the hop to preserve once the requesting file is
// relocated to /runtime).
func resolveSharedLibrary(imageRoot, soname string, searchDirs []originSearchDir) (imagePath, diskPath string, viaOrigin bool, relFromOrigin string, err error) {
	for _, dir := range searchDirs {
		candidate := filepath.Join(imageRoot, filepath.FromSlash(strings.TrimPrefix(dir.diskDir, "/")), soname)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if dir.template == "" {
			imageRel, relErr := filepath.Rel(imageRoot, candidate)
			if relErr != nil {
				return "", "", false, "", relErr
			}
			return "/" + filepath.ToSlash(imageRel), candidate, false, "", nil
		}
		imageRel, relErr := filepath.Rel(imageRoot, candidate)
		if relErr != nil {
			return "", "", false, "", relErr
		}
		return "/" + filepath.ToSlash(imageRel), candidate, true, path.Join(dir.template, soname), nil
	}
	for _, dir := range standardLibraryDirs {
		candidate := filepath.Join(imageRoot, dir, soname)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			return "/" + dir + "/" + soname, candidate, false, "", nil
		}
	}
	return "", "", false, "", fmt.Errorf("not found under %v or %v", searchDirs, standardLibraryDirs)
}
