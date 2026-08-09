// Package sbom generates a native software bill of materials: an
// inventory of local files correlated with their detected kind and ELF
// dependencies via internal/detect.
//
// This does not cover lockfiles, system package metadata, or
// plugin-reported evidence — those are separate, unimplemented
// correlation sources.
package sbom

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/CYPT71/secure-oci-base/internal/detect"
)

const copyBufferSize = 1 << 20

// Component is one file's SBOM entry.
type Component struct {
	Name               string   `json:"name"`
	Digest             string   `json:"digest"`
	Size               int64    `json:"size"`
	Kind               string   `json:"kind"`
	Evidence           []string `json:"evidence,omitempty"`
	NativeDependencies []string `json:"native_dependencies,omitempty"`
}

type Package struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ecosystem string `json:"ecosystem"`
	Source    string `json:"source"`
}

// Document is a native SBOM.
type Document struct {
	Components []Component `json:"components"`
	Packages   []Package   `json:"packages,omitempty"`
}

// Generate builds a Document for paths (component name -> local file
// path), sorted by name so the result is deterministic regardless of map
// iteration order.
func Generate(paths map[string]string) (Document, error) {
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)

	components := make([]Component, 0, len(names))
	var packages []Package
	for _, name := range names {
		component, err := inspect(name, paths[name])
		if err != nil {
			return Document{}, err
		}
		components = append(components, component)
		found, err := inspectPackageMetadata(name, paths[name])
		if err != nil {
			return Document{}, err
		}
		packages = append(packages, found...)
	}
	packages = canonicalPackages(packages)
	return Document{Components: components, Packages: packages}, nil
}

func inspect(name, path string) (Component, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Component{}, fmt.Errorf("sbom: component %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return Component{}, fmt.Errorf("sbom: component %q: %q is not a regular file", name, path)
	}
	digest, err := hashFile(path)
	if err != nil {
		return Component{}, fmt.Errorf("sbom: component %q: %w", name, err)
	}
	result, err := detect.Path(path)
	if err != nil {
		return Component{}, fmt.Errorf("sbom: component %q: detect: %w", name, err)
	}
	return Component{
		Name:               name,
		Digest:             digest,
		Size:               info.Size(),
		Kind:               result.Kind,
		Evidence:           result.Evidence,
		NativeDependencies: result.NativeDeps,
	}, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, file, make([]byte, copyBufferSize)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

// Write encodes doc as JSON to w.
func Write(w io.Writer, doc Document) error {
	return json.NewEncoder(w).Encode(doc)
}
