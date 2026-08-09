// Package sbom is the application-layer service behind `pf sbom` -
// Sanetizer-todo.md item 8 ("la CLI doit devenir une façade"), the same
// extraction internal/app/doctor already did for `pf doctor`.
// cmd/platform-factory/sbom.go now only parses flags, calls Service,
// and formats the result (text vs JSON is a presentation concern, so it
// stays in the CLI layer); path collection and SBOM generation live
// here, testable without going through the CLI.
package sbom

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	engine "github.com/CYPT71/secure-oci-base/internal/sbom"
)

// Document is re-exported so callers of this package never need to
// import internal/sbom directly.
type Document = engine.Document

// Service holds every dependency CollectPaths needs, injectable so
// tests never touch the real filesystem.
type Service struct {
	// Stat reports file info for a path - normally os.Stat.
	Stat func(name string) (os.FileInfo, error)
	// WalkDir walks a directory tree - normally filepath.WalkDir.
	WalkDir func(root string, fn fs.WalkDirFunc) error
}

// New returns a Service wired to the real filesystem.
func New() Service {
	return Service{Stat: os.Stat, WalkDir: filepath.WalkDir}
}

// CollectPaths maps each argument to component entries: a regular file
// becomes one component keyed by its cleaned path, a directory
// contributes every regular file it contains, keyed by path. Keying by
// on-disk path keeps component names unique across arguments and makes
// the sorted output deterministic. Non-regular entries (symlinks,
// device nodes, sockets) are skipped, since only regular files have
// content to hash and classify.
func (s Service) CollectPaths(args []string) (map[string]string, error) {
	paths := map[string]string{}
	for _, arg := range args {
		info, err := s.Stat(arg)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			paths[filepath.Clean(arg)] = arg
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%q is neither a regular file nor a directory", arg)
		}
		walkErr := s.WalkDir(arg, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() {
				paths[filepath.Clean(path)] = path
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return paths, nil
}

// Generate produces the SBOM document for the given path set (as
// returned by CollectPaths): each component records the file's sha256
// digest, size, detected kind, and native ELF dependencies. It uses no
// external tools, so the inventory is available on hosts without syft
// and is byte-for-byte deterministic (components are sorted by name).
func (s Service) Generate(paths map[string]string) (Document, error) {
	return engine.Generate(paths)
}

// WriteJSON writes doc to w in the canonical JSON form - so the CLI
// layer never needs to import internal/sbom directly just to format
// output, matching item 9's cmd -> app -> domain import direction.
func (s Service) WriteJSON(w io.Writer, doc Document) error {
	return engine.Write(w, doc)
}
