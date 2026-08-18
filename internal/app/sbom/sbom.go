// Package sbom is the application-layer service behind `pf sbom` -
// extraction internal/app/doctor already did for `pf doctor`.
// cmd/platform-factory/sbom.go now only parses flags, calls Service
// (the interface New returns), and formats the result (text vs JSON is
// a presentation concern, so it stays in the CLI layer); path
// collection and SBOM generation live here, testable without going
// through the CLI.
package sbom

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	engine "github.com/CYPT71/platform-factory/internal/sbom"
)

// Document is re-exported so callers of this package never need to
// import internal/sbom directly.
type Document = engine.Document

// Service is the narrow contract cmd/platform-factory depends on for
// SBOM path collection and generation.
type Service interface {
	CollectPaths(args []string) (map[string]string, error)
	Generate(paths map[string]string) (Document, error)
	WriteJSON(w io.Writer, doc Document) error
}

// service is Service's only implementation, its filesystem dependencies
// unexported - a test that needs a fake constructs a service literal
// directly (same package, unexported fields still visible).
type service struct {
	// stat reports file info for a path - normally os.Stat.
	stat func(name string) (os.FileInfo, error)
	// walkDir walks a directory tree - normally filepath.WalkDir.
	walkDir func(root string, fn fs.WalkDirFunc) error
}

// New returns a Service wired to the real filesystem.
func New() Service {
	return &service{stat: os.Stat, walkDir: filepath.WalkDir}
}

// CollectPaths maps each argument to component entries: a regular file
// becomes one component keyed by its cleaned path, a directory
// contributes every regular file it contains, keyed by path. Keying by
// on-disk path keeps component names unique across arguments and makes
// the sorted output deterministic. Non-regular entries (symlinks,
// device nodes, sockets) are skipped, since only regular files have
// content to hash and classify.
func (s *service) CollectPaths(args []string) (map[string]string, error) {
	paths := map[string]string{}
	for _, arg := range args {
		info, err := s.stat(arg)
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
		walkErr := s.walkDir(arg, func(path string, entry fs.DirEntry, err error) error {
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
func (s *service) Generate(paths map[string]string) (Document, error) {
	return engine.Generate(paths)
}

// WriteJSON writes doc in canonical JSON form.
func (s *service) WriteJSON(w io.Writer, doc Document) error {
	return engine.Write(w, doc)
}
